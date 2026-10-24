package renewal

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// staticJar always returns one cookie, so a client that used it would send a
// Cookie header. The fetcher must drop the jar.
type staticJar struct{ cookie *http.Cookie }

func (j staticJar) SetCookies(*url.URL, []*http.Cookie) {}
func (j staticJar) Cookies(*url.URL) []*http.Cookie     { return []*http.Cookie{j.cookie} }

const livePageFixture = `<!doctype html>
<html><head>
<script>var app = {config: {}};</script>
<script>window.__APP__ = {selectedPackage: {"ProductType":"AJSPremium"}, pageData:{"purchaseDisabled":false,"purchaseData":{"Lang":"zh_cn","Products":{"AJSPremium":{"IsSoldOut":false,"Title":"爱加速会员","Type":"AJSPremium","Prices":{"generic":{"CNY":{"M1":{"Price":20,"SaveMoney":5},"M3":{"Price":53,"SaveMoney":22},"M12":{"Price":198,"SaveMoney":102},"H6":{"Price":1,"SaveMoney":0,"SpecialType":"TrialPackage"}}},"special/trial":{"CNY":{"H6":{"Price":1,"SaveMoney":0,"SpecialType":"TrialPackage"}}},"auto":{"CNY":{"M1":{"Price":20,"SaveMoney":5},"M3":{"Price":53,"SaveMoney":22}}}}}},"ServicePeriodTitles":{"M1":"30天"}}}, messages:[]}</script>
</head><body></body></html>`

func TestParsePageLiveFixture(t *testing.T) {
	plans, err := ParsePage([]byte(livePageFixture))
	if err != nil {
		t.Fatalf("ParsePage: %v", err)
	}
	if len(plans) != 4 {
		t.Fatalf("len(plans) = %d, want 4: %+v", len(plans), plans)
	}
	wantIDs := []string{"M1", "M3", "M12", "H6"}
	for i, id := range wantIDs {
		if plans[i].ID != id {
			t.Fatalf("plans[%d].ID = %q, want %q", i, plans[i].ID, id)
		}
	}

	m1 := plans[0]
	if m1.Label != "1 month" || m1.Months != 1 || m1.PriceCNY != 20 {
		t.Fatalf("M1 = %+v", m1)
	}
	if m1.AutoRenewPriceCNY == nil || *m1.AutoRenewPriceCNY != 20 {
		t.Fatalf("M1 auto-renew = %v, want 20", m1.AutoRenewPriceCNY)
	}
	if m1.Trial {
		t.Fatalf("M1 marked trial")
	}
	if m1.Notes != "save ¥5" {
		t.Fatalf("M1 notes = %q, want save ¥5", m1.Notes)
	}

	if plans[2].AutoRenewPriceCNY != nil {
		t.Fatalf("M12 auto-renew = %v, want nil", *plans[2].AutoRenewPriceCNY)
	}

	trial := plans[3]
	if trial.ID != "H6" || !trial.Trial || trial.Months != 0 || trial.PriceCNY != 1 {
		t.Fatalf("H6 = %+v", trial)
	}
	if trial.Notes != "TrialPackage" {
		t.Fatalf("H6 notes = %q, want TrialPackage", trial.Notes)
	}
}

func TestParsePageToleratesAssignmentSpacing(t *testing.T) {
	page := []byte(`<script>var state = {}; pageData = { "purchaseData": { "Products": { "AJSPremium": { "Prices": { "generic": { "CNY": { "M3": { "Price": "53" } } } } } } } };</script>`)
	plans, err := ParsePage(page)
	if err != nil {
		t.Fatalf("ParsePage: %v", err)
	}
	if len(plans) != 1 || plans[0].ID != "M3" || plans[0].PriceCNY != 53 {
		t.Fatalf("plans = %+v", plans)
	}
}

func TestParsePageSkipsDecoyMarker(t *testing.T) {
	page := []byte(`<script>var hint = "pageData:{not json}";</script><script>pageData:{"purchaseData":{"Products":{"AJSPremium":{"Prices":{"generic":{"CNY":{"M1":{"Price":20}}}}}}}}</script>`)
	plans, err := ParsePage(page)
	if err != nil {
		t.Fatalf("ParsePage: %v", err)
	}
	if len(plans) != 1 || plans[0].ID != "M1" {
		t.Fatalf("plans = %+v", plans)
	}
}

func TestParsePageMalformed(t *testing.T) {
	cases := map[string]string{
		"truncated":     `<script>pageData:{"purchaseData":{"Products":{"AJSPremium":{"Prices":{"generic":</script>`,
		"no marker":     `<html><body>maintenance</body></html>`,
		"no product":    `<script>pageData:{"purchaseData":{"Products":{}}}</script>`,
		"no prices":     `<script>pageData:{"purchaseData":{"Products":{"AJSPremium":{}}}}</script>`,
		"reshaped":      `<script>pageData:{"purchaseData":{"Products":{"AJSPremium":{"Prices":[]}}}}</script>`,
		"empty generic": `<script>pageData:{"purchaseData":{"Products":{"AJSPremium":{"Prices":{"generic":{"CNY":{}}}}}}}</script>`,
	}
	for name, page := range cases {
		t.Run(name, func(t *testing.T) {
			if plans, err := ParsePage([]byte(page)); err == nil {
				t.Fatalf("ParsePage returned %+v, want error", plans)
			}
		})
	}
}

func TestFetchIsAnonymous(t *testing.T) {
	requests := make(chan *http.Request, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Clone(context.Background())
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, livePageFixture)
	}))
	defer server.Close()

	fetcher := NewFetcher(Config{BaseURL: server.URL + "/"})
	plans, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(plans) != 4 {
		t.Fatalf("len(plans) = %d, want 4", len(plans))
	}
	request := <-requests
	if request.URL.Path != "/purchase" {
		t.Fatalf("path = %q, want /purchase", request.URL.Path)
	}
	if request.URL.RawQuery != "" {
		t.Fatalf("query = %q, want empty", request.URL.RawQuery)
	}
	if len(request.Cookies()) != 0 {
		t.Fatalf("cookies = %v, want none", request.Cookies())
	}
	for _, header := range []string{"Authorization", "Cookie", "X-Auth-Token"} {
		if got := request.Header.Get(header); got != "" {
			t.Fatalf("%s = %q, want empty", header, got)
		}
	}
	if ua := request.Header.Get("User-Agent"); !strings.Contains(ua, "ajs-accountd") {
		t.Fatalf("User-Agent = %q, want it to identify ajs-accountd", ua)
	}
}

func TestFetchDropsCallerCookieJar(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.Cookies()) != 0 {
			t.Errorf("cookies = %v, want none", r.Cookies())
		}
		_, _ = io.WriteString(w, livePageFixture)
	}))
	defer server.Close()

	jar := staticJar{cookie: &http.Cookie{Name: "sid", Value: "secret"}}
	client := &http.Client{Jar: jar}
	fetcher := NewFetcher(Config{BaseURL: server.URL, HTTPClient: client})
	if _, err := fetcher.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if client.Jar != jar {
		t.Fatal("NewFetcher mutated the caller's HTTPClient")
	}
}

func TestFetchReportsStatusWithoutURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	fetcher := NewFetcher(Config{BaseURL: server.URL})
	_, err := fetcher.Fetch(context.Background())
	if err == nil {
		t.Fatal("Fetch succeeded on 502")
	}
	message := err.Error()
	if !strings.Contains(message, "status 502") {
		t.Fatalf("error = %q, want status 502", message)
	}
	for _, forbidden := range []string{"_client_login", "token=", "http://", "https://"} {
		if strings.Contains(message, forbidden) {
			t.Fatalf("error leaks %q: %s", forbidden, message)
		}
	}
}

func TestFetchHonoursTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	fetcher := NewFetcher(Config{BaseURL: server.URL, Timeout: 20 * time.Millisecond})
	start := time.Now()
	if _, err := fetcher.Fetch(context.Background()); err == nil {
		t.Fatal("Fetch succeeded against a stalled server")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Fetch took %s, want the 20ms timeout to bound it", elapsed)
	}
}

// TestFetchRefusesRedirect pins the redirect policy: the catalogue lives at
// one fixed URL, so a 302 is reported as a status error instead of being
// followed to another host. The supplied client follows redirects by default;
// the fetcher must override that on its copy, not mutate the caller's client.
func TestFetchRefusesRedirect(t *testing.T) {
	var targetHits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		_, _ = io.WriteString(w, livePageFixture)
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/purchase", http.StatusFound)
	}))
	defer redirector.Close()

	followed := false
	permissive := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			followed = true
			return nil
		},
	}
	fetcher := NewFetcher(Config{BaseURL: redirector.URL, HTTPClient: permissive})
	_, err := fetcher.Fetch(context.Background())
	if err == nil {
		t.Fatal("Fetch followed a redirect and parsed the target page")
	}
	if !strings.Contains(err.Error(), "status 302") {
		t.Fatalf("error = %q, want status 302", err)
	}
	if strings.Contains(err.Error(), redirector.URL) || strings.Contains(err.Error(), target.URL) {
		t.Fatalf("error leaks a redirect URL: %s", err)
	}
	if targetHits.Load() != 0 {
		t.Fatalf("redirect target was requested %d times, want 0", targetHits.Load())
	}
	if permissive.CheckRedirect == nil {
		t.Fatal("NewFetcher mutated the caller's HTTPClient")
	}
	if followed {
		t.Fatal("the fetcher's copy used the caller's permissive redirect policy")
	}
}
