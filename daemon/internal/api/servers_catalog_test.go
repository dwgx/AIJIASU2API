package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"ajsaccountd/internal/account"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/store"
	"ajsproxy/apifacade"
)

// catalogFixtureSize is the real vendor catalog size the pagination contract
// is pinned against.
const catalogFixtureSize = 2814

// catalogLab overrides only Servers; the embedded interface keeps the fake
// small while satisfying LabReader.
type catalogLab struct {
	LabReader
	view model.ServersView
	err  error
}

func (c *catalogLab) Servers(_ context.Context, _ string) (model.ServersView, error) {
	if c.err != nil {
		return model.ServersView{}, c.err
	}
	view := c.view
	view.Servers = append([]model.ServerView(nil), c.view.Servers...)
	return view, nil
}

// catalogLoginResults is the retained-login seam the runtime view reads.
type catalogLoginResults struct {
	result account.LoginResult
	ok     bool
}

func (c *catalogLoginResults) Result(string) (account.LoginResult, bool) {
	return c.result, c.ok
}

// newCatalogFixture builds n deterministic servers. Regions, statuses and
// selection cycle; titles are a permutation of "Catalog 0001".."Catalog nnnn"
// and Order is n..1, so every sort key has a checkable expected order.
func newCatalogFixture(n int) model.ServersView {
	regions := []string{"cn", "us", "jp", "de", "sg", "hk", "br", "gb", "unknown"}
	statuses := []string{"ok", "ok", "ok", "full", "maintenance"}
	servers := make([]model.ServerView, n)
	for i := range n {
		servers[i] = model.ServerView{
			ID:            fmt.Sprintf("srv-%04d", i+1),
			Title:         fmt.Sprintf("Catalog %04d", ((i*17)%n)+1),
			Order:         strconv.Itoa(n - i),
			Region:        regions[i%len(regions)],
			Host:          fmt.Sprintf("10.%d.%d.%d", (i/65536)%256, (i/256)%256, i%256),
			Port:          443,
			Status:        statuses[i%len(statuses)],
			NodeID:        fmt.Sprintf("acc-01:srv-%04d", i+1),
			Selected:      i%3 == 0,
			BlockingMarks: []string{},
		}
	}
	return model.ServersView{
		Enabled:   true,
		AccountID: "acc-01",
		FetchedAt: "2026-10-24T12:00:00Z",
		Servers:   servers,
	}
}

func catalogGET(t *testing.T, ts *httptest.Server, path string) model.ServersView {
	t.Helper()
	status, body := labGET(t, ts, path, true)
	if status != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200: %s", path, status, body)
	}
	var view model.ServersView
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("decode %s: %v (%s)", path, err, body)
	}
	return view
}

// catalogAll pages through the whole filtered catalog at the maximum page
// size and checks every page reports progress and a consistent total.
func catalogAll(t *testing.T, ts *httptest.Server, query string) []model.ServerView {
	t.Helper()
	var all []model.ServerView
	offset := 0
	for range 100 {
		path := fmt.Sprintf("/v1/accounts/acc-01/servers?offset=%d&limit=%d", offset, serverCatalogMaxLimit)
		if query != "" {
			path += "&" + query
		}
		view := catalogGET(t, ts, path)
		all = append(all, view.Servers...)
		if !view.HasMore {
			if view.NextOffset != view.Total {
				t.Fatalf("last page nextOffset = %d, want total %d", view.NextOffset, view.Total)
			}
			return all
		}
		if view.NextOffset <= offset || len(view.Servers) == 0 {
			t.Fatalf("pagination made no progress: offset=%d next=%d rows=%d", offset, view.NextOffset, len(view.Servers))
		}
		offset = view.NextOffset
	}
	t.Fatal("pagination did not terminate")
	return nil
}

func TestServerCatalogDefaultsAndPagination(t *testing.T) {
	fixture := newCatalogFixture(catalogFixtureSize)
	ts := newLabTestServer(t, &catalogLab{view: fixture}, store.New())

	view := catalogGET(t, ts, "/v1/accounts/acc-01/servers")
	if view.Total != catalogFixtureSize || view.Offset != 0 || view.Limit != serverCatalogDefaultLimit ||
		view.NextOffset != serverCatalogDefaultLimit || !view.HasMore {
		t.Fatalf("default page = total:%d offset:%d limit:%d next:%d hasMore:%v",
			view.Total, view.Offset, view.Limit, view.NextOffset, view.HasMore)
	}
	if len(view.Servers) != serverCatalogDefaultLimit {
		t.Fatalf("default page rows = %d, want %d", len(view.Servers), serverCatalogDefaultLimit)
	}
	if view.Servers[0].ID != "srv-2814" || view.Servers[len(view.Servers)-1].ID != "srv-2765" {
		t.Fatalf("default order = %s..%s, want srv-2814..srv-2765",
			view.Servers[0].ID, view.Servers[len(view.Servers)-1].ID)
	}

	last := catalogGET(t, ts, fmt.Sprintf("/v1/accounts/acc-01/servers?offset=%d", catalogFixtureSize-14))
	if last.Total != catalogFixtureSize || last.Offset != catalogFixtureSize-14 || last.Limit != serverCatalogDefaultLimit {
		t.Fatalf("last page header = %+v", last)
	}
	if len(last.Servers) != 14 || last.HasMore || last.NextOffset != catalogFixtureSize {
		t.Fatalf("last page = rows:%d hasMore:%v next:%d", len(last.Servers), last.HasMore, last.NextOffset)
	}

	beyond := catalogGET(t, ts, "/v1/accounts/acc-01/servers?offset=99999")
	if beyond.Total != catalogFixtureSize || beyond.Offset != 99999 || beyond.HasMore ||
		beyond.NextOffset != catalogFixtureSize || len(beyond.Servers) != 0 {
		t.Fatalf("beyond-end page = %+v", beyond)
	}
	if beyond.Servers == nil || beyond.Regions == nil {
		t.Fatalf("empty page must serialize [] not null: servers=%v regions=%v", beyond.Servers, beyond.Regions)
	}

	all := catalogAll(t, ts, "")
	if len(all) != catalogFixtureSize {
		t.Fatalf("paged catalog = %d rows, want %d", len(all), catalogFixtureSize)
	}
	if all[0].ID != "srv-2814" || all[len(all)-1].ID != "srv-0001" {
		t.Fatalf("paged order = %s..%s", all[0].ID, all[len(all)-1].ID)
	}
}

func TestServerCatalogRegionsSummary(t *testing.T) {
	fixture := newCatalogFixture(catalogFixtureSize)
	ts := newLabTestServer(t, &catalogLab{view: fixture}, store.New())

	view := catalogGET(t, ts, "/v1/accounts/acc-01/servers?limit=1")
	if len(view.Regions) != 9 {
		t.Fatalf("regions = %+v, want 9 codes", view.Regions)
	}
	sum := 0
	want := map[string]int{
		"br": 312, "cn": 313, "de": 313, "gb": 312, "hk": 313,
		"jp": 313, "sg": 313, "unknown": 312, "us": 313,
	}
	for _, region := range view.Regions {
		sum += region.Count
		if want[region.Code] != region.Count {
			t.Fatalf("region %s count = %d, want %d", region.Code, region.Count, want[region.Code])
		}
	}
	if sum != catalogFixtureSize {
		t.Fatalf("region counts sum = %d, want %d", sum, catalogFixtureSize)
	}
	if len(view.Servers) != 1 {
		t.Fatalf("limit=1 rows = %d", len(view.Servers))
	}

	filtered := catalogGET(t, ts, "/v1/accounts/acc-01/servers?region=cn")
	if len(filtered.Regions) != 1 || filtered.Regions[0].Code != "cn" || filtered.Regions[0].Count != 313 {
		t.Fatalf("filtered regions = %+v, want only cn:313", filtered.Regions)
	}
}

func TestServerCatalogFilters(t *testing.T) {
	fixture := newCatalogFixture(catalogFixtureSize)
	ts := newLabTestServer(t, &catalogLab{view: fixture}, store.New())

	cases := []struct {
		name  string
		query string
		want  int
		check func(t *testing.T, servers []model.ServerView)
	}{
		{
			name:  "q matches id",
			query: "q=srv-0001",
			want:  1,
			check: func(t *testing.T, servers []model.ServerView) {
				if servers[0].ID != "srv-0001" {
					t.Fatalf("id = %s", servers[0].ID)
				}
			},
		},
		{
			name:  "q is case insensitive",
			query: "q=CATALOG+0001",
			want:  1,
			check: func(t *testing.T, servers []model.ServerView) {
				if servers[0].Title != "Catalog 0001" {
					t.Fatalf("title = %s", servers[0].Title)
				}
			},
		},
		{
			name:  "q matches host",
			query: "q=10.0.1.0",
			want:  1,
			check: func(t *testing.T, servers []model.ServerView) {
				if servers[0].Host != "10.0.1.0" {
					t.Fatalf("host = %s", servers[0].Host)
				}
			},
		},
		{
			name:  "region lowercase",
			query: "region=cn",
			want:  313,
			check: func(t *testing.T, servers []model.ServerView) {
				for _, server := range servers {
					if server.Region != "cn" {
						t.Fatalf("region filter leaked %s", server.Region)
					}
				}
			},
		},
		{
			name:  "region uppercase",
			query: "region=CN",
			want:  313,
			check: nil,
		},
		{
			name:  "status ok",
			query: "status=ok",
			want:  1689,
			check: func(t *testing.T, servers []model.ServerView) {
				for _, server := range servers {
					if server.Status != "ok" {
						t.Fatalf("status filter leaked %s", server.Status)
					}
				}
			},
		},
		{
			name:  "status uppercase",
			query: "status=FULL",
			want:  563,
			check: nil,
		},
		{
			name:  "selected true",
			query: "selected=true",
			want:  938,
			check: func(t *testing.T, servers []model.ServerView) {
				for _, server := range servers {
					if !server.Selected {
						t.Fatalf("selected filter leaked %s", server.ID)
					}
				}
			},
		},
		{
			name:  "selected false",
			query: "selected=false",
			want:  1876,
			check: nil,
		},
		{
			name:  "combined",
			query: "region=cn&status=ok&selected=true",
			want:  -1,
			check: func(t *testing.T, servers []model.ServerView) {
				for _, server := range servers {
					if server.Region != "cn" || server.Status != "ok" || !server.Selected {
						t.Fatalf("combined filter leaked %+v", server)
					}
				}
			},
		},
		{
			name:  "q no match",
			query: "q=no-such-server",
			want:  0,
			check: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			view := catalogGET(t, ts, "/v1/accounts/acc-01/servers?limit=500&"+tc.query)
			want := tc.want
			if want < 0 {
				want = 0
				for _, server := range fixture.Servers {
					if server.Region == "cn" && server.Status == "ok" && server.Selected {
						want++
					}
				}
			}
			if view.Total != want {
				t.Fatalf("total = %d, want %d", view.Total, want)
			}
			if tc.check != nil && len(view.Servers) > 0 {
				tc.check(t, view.Servers)
			}
			if want > 0 && len(view.Servers) == 0 {
				t.Fatalf("total = %d but no rows", want)
			}
		})
	}
}

func TestServerCatalogSort(t *testing.T) {
	fixture := newCatalogFixture(catalogFixtureSize)
	ts := newLabTestServer(t, &catalogLab{view: fixture}, store.New())

	t.Run("order is numeric and default", func(t *testing.T) {
		all := catalogAll(t, ts, "sort=order")
		if len(all) != catalogFixtureSize {
			t.Fatalf("rows = %d", len(all))
		}
		for i, server := range all {
			wantID := fmt.Sprintf("srv-%04d", catalogFixtureSize-i)
			if server.ID != wantID {
				t.Fatalf("row %d = %s, want %s (numeric ord order)", i, server.ID, wantID)
			}
		}
	})

	t.Run("title", func(t *testing.T) {
		all := catalogAll(t, ts, "sort=TITLE")
		for i := 1; i < len(all); i++ {
			if strings.ToLower(all[i-1].Title) > strings.ToLower(all[i].Title) {
				t.Fatalf("title order at %d: %q > %q", i, all[i-1].Title, all[i].Title)
			}
		}
		if all[0].Title != "Catalog 0001" || all[len(all)-1].Title != "Catalog 2814" {
			t.Fatalf("title bounds = %q..%q", all[0].Title, all[len(all)-1].Title)
		}
	})

	t.Run("host", func(t *testing.T) {
		all := catalogAll(t, ts, "sort=host")
		for i := 1; i < len(all); i++ {
			if all[i-1].Host > all[i].Host {
				t.Fatalf("host order at %d: %q > %q", i, all[i-1].Host, all[i].Host)
			}
		}
	})

	t.Run("status", func(t *testing.T) {
		all := catalogAll(t, ts, "sort=status")
		for i := 1; i < len(all); i++ {
			if strings.ToLower(all[i-1].Status) > strings.ToLower(all[i].Status) {
				t.Fatalf("status order at %d: %q > %q", i, all[i-1].Status, all[i].Status)
			}
		}
		if all[0].Status != "full" || all[len(all)-1].Status != "ok" {
			t.Fatalf("status bounds = %q..%q", all[0].Status, all[len(all)-1].Status)
		}
	})

	t.Run("region breaks ties by id", func(t *testing.T) {
		all := catalogAll(t, ts, "sort=region")
		for i := 1; i < len(all); i++ {
			previous, current := all[i-1], all[i]
			if previous.Region > current.Region {
				t.Fatalf("region order at %d: %q > %q", i, previous.Region, current.Region)
			}
			if previous.Region == current.Region && previous.ID > current.ID {
				t.Fatalf("id tie-break at %d: %q > %q", i, previous.ID, current.ID)
			}
		}
	})
}

func TestServerCatalogLimitAndInvalidParams(t *testing.T) {
	fixture := newCatalogFixture(catalogFixtureSize)
	ts := newLabTestServer(t, &catalogLab{view: fixture}, store.New())

	view := catalogGET(t, ts, "/v1/accounts/acc-01/servers?limit=500")
	if view.Limit != 500 || len(view.Servers) != 500 || !view.HasMore {
		t.Fatalf("limit=500 page = %+v", view)
	}

	invalid := []struct {
		query   string
		message string
	}{
		{"limit=501", "limit"},
		{"limit=0", "limit"},
		{"limit=-1", "limit"},
		{"limit=abc", "limit"},
		{"offset=-1", "offset"},
		{"offset=abc", "offset"},
		{"selected=maybe", "selected"},
		{"selected=1", "selected"},
		{"sort=vsid", "sort"},
	}
	for _, tc := range invalid {
		t.Run(tc.query, func(t *testing.T) {
			status, body := labGET(t, ts, "/v1/accounts/acc-01/servers?"+tc.query, true)
			if status != http.StatusBadRequest {
				t.Fatalf("%s = %d, want 400: %s", tc.query, status, body)
			}
			var payload errorResponse
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("decode error: %v (%s)", err, body)
			}
			if payload.Error.Code != CodeInvalidArgument || !strings.Contains(payload.Error.Message, tc.message) {
				t.Fatalf("%s error = %+v, want INVALID_ARGUMENT mentioning %q", tc.query, payload.Error, tc.message)
			}
		})
	}

	// Validation runs before the disabled body, so a bad query is a 400 even
	// when no Lab reader is wired.
	plain := newLabTestServer(t, nil, store.New())
	status, body := labGET(t, plain, "/v1/accounts/acc-01/servers?limit=501", true)
	if status != http.StatusBadRequest {
		t.Fatalf("disabled reader with bad limit = %d, want 400: %s", status, body)
	}
}

func TestServerCatalogBackwardCompatibleShape(t *testing.T) {
	fixture := newCatalogFixture(3)
	ts := newLabTestServer(t, &catalogLab{view: fixture}, store.New())

	status, body := labGET(t, ts, "/v1/accounts/acc-01/servers", true)
	if status != http.StatusOK {
		t.Fatalf("servers = %d: %s", status, body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode envelope: %v (%s)", err, body)
	}
	for _, key := range []string{
		"enabled", "accountId", "fetchedAt", "stale", "servers",
		"total", "offset", "limit", "nextOffset", "hasMore", "regions",
	} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("response missing %q: %s", key, body)
		}
	}

	var envelope struct {
		Servers []map[string]json.RawMessage `json:"servers"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode servers: %v", err)
	}
	if len(envelope.Servers) != 3 {
		t.Fatalf("servers = %d", len(envelope.Servers))
	}
	for _, key := range []string{
		"id", "title", "region", "host", "port", "status", "nodeId", "nodeState",
		"selected", "skippedByLimit", "memberOnly", "fullLoad", "blockingMarks",
		"hasPingSecret", "latencyMs",
	} {
		if _, ok := envelope.Servers[0][key]; !ok {
			t.Fatalf("server missing legacy field %q: %s", key, body)
		}
	}
	if _, ok := envelope.Servers[0]["order"]; ok {
		t.Fatalf("catalog metadata order leaked into the response: %s", body)
	}
}

func TestServerCatalogNoRetainedLogin(t *testing.T) {
	st := store.New()
	lab := runtime.NewLabView(runtime.LabConfig{
		Store:  st,
		Logins: &catalogLoginResults{ok: false},
	})
	ts := newLabTestServer(t, lab, st)

	view := catalogGET(t, ts, "/v1/accounts/acc-01/servers")
	if !view.Enabled || !view.Stale || view.Reason == "" {
		t.Fatalf("no-login header = %+v, want enabled stale with reason", view)
	}
	if view.Total != 0 || view.Offset != 0 || view.Limit != serverCatalogDefaultLimit ||
		view.NextOffset != 0 || view.HasMore || len(view.Servers) != 0 {
		t.Fatalf("no-login page = %+v", view)
	}
	if view.Servers == nil || view.Regions == nil {
		t.Fatalf("no-login body must serialize [] not null: servers=%v regions=%v", view.Servers, view.Regions)
	}

	status, body := labGET(t, ts, "/v1/accounts/missing/servers?limit=10", true)
	if status != http.StatusNotFound {
		t.Fatalf("missing account = %d, want 404: %s", status, body)
	}
}

func TestServerCatalogNeverExposesPingSecret(t *testing.T) {
	const secret = "ping-secret-value"
	st := store.New()
	servers := make(map[string]apifacade.Server, catalogFixtureSize)
	for i := range catalogFixtureSize {
		id := fmt.Sprintf("srv-%04d", i+1)
		servers[id] = apifacade.Server{
			ID:   id,
			Svr:  fmt.Sprintf("203.0.113.%d", i%250+1),
			Tit:  fmt.Sprintf("Catalog %04d", i+1),
			Ctry: "CN",
			Sts:  "ok",
			Ord:  strconv.Itoa(catalogFixtureSize - i),
			Par: apifacade.ServerPar{FV: apifacade.ServerFV{
				P:  apifacade.FlexInt(443),
				PS: secret,
			}},
		}
	}
	logins := &catalogLoginResults{
		ok: true,
		result: account.LoginResult{
			Servers:   servers,
			FetchedAt: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC),
		},
	}
	lab := runtime.NewLabView(runtime.LabConfig{Store: st, Logins: logins})
	ts := newLabTestServer(t, lab, st)

	view := catalogGET(t, ts, "/v1/accounts/acc-01/servers?limit=500&sort=order")
	if view.Total != catalogFixtureSize || len(view.Servers) != 500 {
		t.Fatalf("runtime catalog page = total:%d rows:%d", view.Total, len(view.Servers))
	}
	if view.Servers[0].ID != "srv-2814" {
		t.Fatalf("sort=order first row = %s, want srv-2814 (vendor ord)", view.Servers[0].ID)
	}
	if !view.Servers[0].HasPingSecret {
		t.Fatalf("first row hasPingSecret = false, want true")
	}

	status, body := labGET(t, ts, "/v1/accounts/acc-01/servers?limit=500", true)
	if status != http.StatusOK {
		t.Fatalf("servers = %d: %s", status, body)
	}
	if strings.Contains(string(body), secret) {
		t.Fatalf("catalog leaked par.fv.ps: %s", body)
	}
}

func geoFixture() model.ServersView {
	return model.ServersView{
		Enabled:   true,
		AccountID: "acc-01",
		FetchedAt: "2026-10-24T12:00:00Z",
		Servers: []model.ServerView{
			{ID: "srv-1", Title: "镇江 #1", Region: "cn", Status: "ok", NodeState: model.NodeLive},
			{ID: "srv-2", Title: "南京 #1", Region: "cn", Status: "ok", NodeState: model.NodeDraining},
			{ID: "srv-3", Title: "杭州 #1", Region: "cn", Status: "ok"},
			// A province short name is not a city: this row stays unresolved.
			{ID: "srv-4", Title: "海南 #1", Region: "cn", Status: "ok"},
			{ID: "srv-5", Title: "海南藏族自治州 #1", Region: "cn", Status: "ok"},
		},
	}
}

func TestServerCatalogGeoSummary(t *testing.T) {
	ts := newLabTestServer(t, &catalogLab{view: geoFixture()}, store.New())

	view := catalogGET(t, ts, "/v1/accounts/acc-01/servers?limit=2")
	geo := view.Geo
	if geo == nil {
		t.Fatal("servers response has no geo summary")
	}
	if geo.Total != 5 || geo.Unknown != 1 {
		t.Fatalf("geo total/unknown = %d/%d, want 5/1", geo.Total, geo.Unknown)
	}
	if len(geo.Live) != 1 || geo.Current == nil || geo.Current.Title != "镇江 #1" {
		t.Fatalf("geo current/live = %+v/%+v, want 镇江 #1", geo.Current, geo.Live)
	}
	byCode := make(map[string]model.CatalogGeoProvince, len(geo.Provinces))
	for _, province := range geo.Provinces {
		byCode[province.Code] = province
	}
	if jiangsu := byCode["32"]; jiangsu.Count != 2 || jiangsu.Connected != 2 ||
		len(jiangsu.Cities) != 2 {
		t.Fatalf("province 32 = %+v, want count 2 connected 2 cities 2", jiangsu)
	}
	if zhejiang := byCode["33"]; zhejiang.Count != 1 {
		t.Fatalf("province 33 = %+v, want count 1", zhejiang)
	}
	if qinghai := byCode["63"]; qinghai.Count != 1 || qinghai.Cities[0].Code != "632500" {
		t.Fatalf("province 63 = %+v, want 海南藏族自治州", qinghai)
	}
	// The page is two rows; the map keeps counting the whole scope.
	if len(view.Servers) != 2 || view.Total != 5 {
		t.Fatalf("page = rows:%d total:%d, want 2/5", len(view.Servers), view.Total)
	}
}

func TestServerCatalogGeoFilters(t *testing.T) {
	ts := newLabTestServer(t, &catalogLab{view: geoFixture()}, store.New())

	jiangsu := catalogGET(t, ts, "/v1/accounts/acc-01/servers?province=32")
	if jiangsu.Total != 2 || len(jiangsu.Servers) != 2 {
		t.Fatalf("province=32 = total:%d rows:%d, want 2/2", jiangsu.Total, len(jiangsu.Servers))
	}
	for _, server := range jiangsu.Servers {
		if server.Title != "镇江 #1" && server.Title != "南京 #1" {
			t.Fatalf("province=32 returned %q", server.Title)
		}
	}
	// The geo summary stays scoped to the search, not the province filter.
	if jiangsu.Geo == nil || jiangsu.Geo.Total != 5 {
		t.Fatalf("province=32 geo total = %+v, want 5", jiangsu.Geo)
	}

	nanjing := catalogGET(t, ts, "/v1/accounts/acc-01/servers?province=32&city=320100")
	if nanjing.Total != 1 || nanjing.Servers[0].Title != "南京 #1" {
		t.Fatalf("city filter = %+v", nanjing.Servers)
	}

	other := catalogGET(t, ts, "/v1/accounts/acc-01/servers?province=00")
	if other.Total != 1 || other.Servers[0].Title != "海南 #1" {
		t.Fatalf("province=00 = %+v, want the unresolved row", other.Servers)
	}

	hainan := catalogGET(t, ts, "/v1/accounts/acc-01/servers?province=46")
	if hainan.Total != 0 || len(hainan.Servers) != 0 {
		t.Fatalf("province=46 = %+v, want no rows (海南 #1 is not Hainan)", hainan.Servers)
	}

	combined := catalogGET(t, ts, "/v1/accounts/acc-01/servers?q=南京&province=32")
	if combined.Total != 1 || combined.Servers[0].ID != "srv-2" {
		t.Fatalf("q+province = %+v", combined.Servers)
	}
}

func TestAccountServersSnapshotID(t *testing.T) {
	lab := &catalogLab{view: geoFixture()}
	connector := &fakeCatalog{snapshotID: "cat-live"}
	server := New(Config{AdminToken: testToken, Lab: lab, Catalog: connector}, store.New(),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	view := catalogGET(t, ts, "/v1/accounts/acc-01/servers?limit=1")
	if view.SnapshotID != "cat-live" {
		t.Fatalf("snapshotId = %q, want cat-live", view.SnapshotID)
	}

	// No connector (login disabled) means nothing can guard a write.
	plain := newLabTestServer(t, lab, store.New())
	if view := catalogGET(t, plain, "/v1/accounts/acc-01/servers?limit=1"); view.SnapshotID != "" {
		t.Fatalf("snapshotId without connector = %q, want empty", view.SnapshotID)
	}

	// A stale catalog retains no result to guard.
	stale := &catalogLab{view: model.ServersView{
		Enabled: true, AccountID: "acc-01", Stale: true, Servers: []model.ServerView{},
	}}
	server = New(Config{AdminToken: testToken, Lab: stale, Catalog: connector}, store.New(),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	staleTS := httptest.NewServer(server)
	t.Cleanup(staleTS.Close)
	if view := catalogGET(t, staleTS, "/v1/accounts/acc-01/servers"); view.SnapshotID != "" {
		t.Fatalf("stale snapshotId = %q, want empty", view.SnapshotID)
	}
}
