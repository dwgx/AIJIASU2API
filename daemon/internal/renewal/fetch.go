package renewal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultTimeout bounds one purchase-page request. Eight seconds is enough
	// for a tens-of-kilobytes HTML page on a slow origin and still returns
	// quickly enough for a panel request.
	DefaultTimeout = 8 * time.Second
	// DefaultUserAgent identifies APC to the vendor without claiming to be a
	// browser or a vendor client.
	DefaultUserAgent = "ajs-accountd-renewal/1.0"
	// maxPageBytes caps the response body. The observed page is under 40 KiB;
	// the cap keeps a hostile or broken origin from growing the daemon's heap.
	maxPageBytes = 4 << 20
)

// Config configures a Fetcher or a Service. The zero value is usable: it points
// at the vendor origin, uses an eight-second timeout, a six-hour TTL (or
// AJS_RENEWAL_TTL) and time.Now.
type Config struct {
	// BaseURL is the vendor origin, without a trailing slash. Empty selects
	// DefaultBaseURL.
	BaseURL string
	// HTTPClient performs the request. Nil builds one with Timeout. A supplied
	// client's cookie jar is dropped: the fetch is anonymous by construction.
	HTTPClient *http.Client
	// UserAgent identifies APC. Empty selects DefaultUserAgent.
	UserAgent string
	// Timeout bounds one request. Non-positive selects DefaultTimeout.
	Timeout time.Duration
	// TTL is how long a live snapshot stays fresh. Non-positive selects
	// AJS_RENEWAL_TTL, or DefaultTTL when the variable is absent or invalid.
	TTL time.Duration
	// Now is the clock used for FetchedAt and expiry. Nil selects time.Now.
	Now func() time.Time
}

// Fetcher performs the anonymous public-page GET and parses the embedded
// catalogue. It holds no state between calls and is safe for concurrent use.
type Fetcher struct {
	baseURL   string
	client    *http.Client
	userAgent string
	timeout   time.Duration
}

// NewFetcher builds a Fetcher from cfg.
func NewFetcher(cfg Config) *Fetcher {
	return newFetcher(normalizeConfig(cfg))
}

func newFetcher(cfg Config) *Fetcher {
	return &Fetcher{
		baseURL:   cfg.BaseURL,
		client:    cfg.HTTPClient,
		userAgent: cfg.UserAgent,
		timeout:   cfg.Timeout,
	}
}

// normalizeConfig fills defaults. It never mutates the caller's HTTPClient.
func normalizeConfig(cfg Config) Config {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if strings.TrimSpace(cfg.UserAgent) == "" {
		cfg.UserAgent = DefaultUserAgent
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.TTL <= 0 {
		cfg.TTL = ttlFromEnv()
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: cfg.Timeout}
	} else {
		client := *cfg.HTTPClient
		// A jar would let a cookie set by the page travel back on a redirect,
		// turning an anonymous GET into a stateful one. The public catalogue
		// needs no cookies.
		client.Jar = nil
		cfg.HTTPClient = &client
	}
	return cfg
}

// Fetch downloads the public purchase page and returns its plan catalogue. It
// sends no cookies, no authorization header and no account identifier.
func (f *Fetcher) Fetch(ctx context.Context) ([]Plan, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()

	// The URL is fixed and carries no query or credential, so wrapping a
	// transport error cannot leak a token into a log line.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.baseURL+purchasePath, nil)
	if err != nil {
		return nil, fmt.Errorf("renewal: build purchase request: %w", err)
	}
	req.Header.Set("User-Agent", f.userAgent)
	req.Header.Set("Accept", "text/html, application/xhtml+xml")

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("renewal: fetch purchase page: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("renewal: purchase page returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes+1))
	if err != nil {
		return nil, fmt.Errorf("renewal: read purchase page: %w", err)
	}
	if len(body) > maxPageBytes {
		return nil, fmt.Errorf("renewal: purchase page exceeds %d bytes", maxPageBytes)
	}
	return ParsePage(body)
}

// ParsePage extracts pageData.purchaseData.Products.AJSPremium.Prices from an
// HTML page. The page embeds the object as JavaScript, so the parser locates
// the pageData assignment and scans a balanced JSON object before decoding.
// A missing, truncated or reshaped blob is an error; Service turns that into a
// cache or static fallback.
func ParsePage(page []byte) ([]Plan, error) {
	raw, err := extractPageData(page)
	if err != nil {
		return nil, fmt.Errorf("renewal: %w", err)
	}
	var data struct {
		PurchaseData struct {
			GenericCurrency string `json:"GenericCurrency"`
			Products        map[string]struct {
				Prices json.RawMessage `json:"Prices"`
			} `json:"Products"`
		} `json:"purchaseData"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("renewal: decode pageData: %w", err)
	}
	product, ok := data.PurchaseData.Products["AJSPremium"]
	if !ok || len(product.Prices) == 0 || bytes.Equal(bytes.TrimSpace(product.Prices), []byte("null")) {
		return nil, errors.New("renewal: pageData has no AJSPremium prices")
	}
	var variants map[string]map[string]map[string]priceEntry
	dec := json.NewDecoder(bytes.NewReader(product.Prices))
	dec.UseNumber()
	if err := dec.Decode(&variants); err != nil {
		return nil, fmt.Errorf("renewal: decode AJSPremium prices: %w", err)
	}

	baseName, base := pickBase(variants, data.PurchaseData.GenericCurrency)
	if base == nil {
		return nil, errors.New("renewal: AJSPremium prices have no usable currency list")
	}
	auto, _ := pickCurrency(variants["auto"], data.PurchaseData.GenericCurrency)
	if baseName == "auto" {
		auto = nil
	}

	plans := make([]Plan, 0, len(base))
	for id, entry := range base {
		if id == "" {
			continue
		}
		price, ok := entry.price()
		if !ok || price <= 0 {
			continue
		}
		plan := Plan{
			ID:       id,
			Label:    periodLabel(id),
			Months:   periodMonths(id),
			PriceCNY: price,
			Trial:    entry.trial(baseName),
			Notes:    entry.notes(),
		}
		if autoEntry, ok := auto[id]; ok {
			if autoPrice, ok := autoEntry.price(); ok && autoPrice > 0 {
				plan.AutoRenewPriceCNY = &autoPrice
			}
		}
		plans = append(plans, plan)
	}
	if len(plans) == 0 {
		return nil, errors.New("renewal: AJSPremium prices contained no usable plans")
	}
	sortPlans(plans)
	return plans, nil
}

// priceEntry is one period's vendor record. Numbers arrive as JSON numbers on
// the observed page and as quoted strings on some reshaped pages; json.Number
// accepts both.
type priceEntry struct {
	Price       json.Number `json:"Price"`
	SaveMoney   json.Number `json:"SaveMoney"`
	SpecialType string      `json:"SpecialType"`
}

func (e priceEntry) price() (int64, bool) {
	if e.Price == "" {
		return 0, false
	}
	value, err := e.Price.Int64()
	if err != nil {
		return 0, false
	}
	return value, true
}

// trial reports whether the period is a trial window. The vendor marks it with
// SpecialType "TrialPackage"; the "special/trial" variant is a second signal.
func (e priceEntry) trial(variant string) bool {
	return strings.Contains(strings.ToLower(variant), "trial") ||
		strings.Contains(strings.ToLower(e.SpecialType), "trial")
}

func (e priceEntry) notes() string {
	parts := make([]string, 0, 2)
	if e.SpecialType != "" {
		parts = append(parts, e.SpecialType)
	}
	if e.SaveMoney != "" {
		if save, err := e.SaveMoney.Int64(); err == nil && save > 0 {
			parts = append(parts, fmt.Sprintf("save ¥%d", save))
		}
	}
	return strings.Join(parts, "; ")
}

// pickBase selects the regular catalogue. "generic" is the observed key; the
// trial and auto variants are fallbacks so a renamed regular variant still
// yields prices instead of an empty catalogue.
func pickBase(variants map[string]map[string]map[string]priceEntry, currency string) (string, map[string]priceEntry) {
	for _, name := range []string{"generic", "special/trial", "auto"} {
		if prices, ok := pickCurrency(variants[name], currency); ok {
			return name, prices
		}
	}
	return "", nil
}

// pickCurrency prefers CNY, then the page's GenericCurrency, then the only
// currency present.
func pickCurrency(currencies map[string]map[string]priceEntry, preferred string) (map[string]priceEntry, bool) {
	if len(currencies) == 0 {
		return nil, false
	}
	for _, name := range []string{"CNY", preferred} {
		if name == "" {
			continue
		}
		if prices, ok := currencies[name]; ok && len(prices) > 0 {
			return prices, true
		}
	}
	if len(currencies) == 1 {
		for _, prices := range currencies {
			if len(prices) > 0 {
				return prices, true
			}
		}
	}
	return nil, false
}

// periodLabel derives a stable, language-neutral label from the period code.
// The dashboard localizes the code itself; this label is the daemon-side
// fallback for clients that only render the snapshot.
func periodLabel(id string) string {
	switch id {
	case "M1":
		return "1 month"
	case "M3":
		return "3 months"
	case "M6":
		return "6 months"
	case "M12":
		return "12 months"
	case "M24":
		return "24 months"
	case "H6":
		return "6-hour trial"
	case "H24":
		return "24-hour trial"
	case "D1":
		return "1 day"
	case "m30":
		return "30 minutes"
	default:
		return id
	}
}

// periodMonths maps a vendor period code to whole months. Short periods
// (hours, days, minutes) and unknown codes report zero.
func periodMonths(id string) int {
	if len(id) < 2 || id[0] != 'M' {
		return 0
	}
	months, err := strconv.Atoi(id[1:])
	if err != nil || months < 0 {
		return 0
	}
	return months
}

// sortPlans puts paid terms first by length, then trials, so the snapshot is
// stable across map iteration order.
func sortPlans(plans []Plan) {
	sort.Slice(plans, func(i, j int) bool {
		if plans[i].Trial != plans[j].Trial {
			return !plans[i].Trial
		}
		if plans[i].Months != plans[j].Months {
			return plans[i].Months < plans[j].Months
		}
		return plans[i].ID < plans[j].ID
	})
}

// extractPageData finds the JSON object assigned to pageData. The marker is
// tried at every occurrence because an unrelated string may mention it first;
// only a balanced object that is valid JSON is accepted.
func extractPageData(page []byte) ([]byte, error) {
	marker := []byte("pageData")
	for offset := 0; offset < len(page); {
		i := bytes.Index(page[offset:], marker)
		if i < 0 {
			break
		}
		i += offset
		after := i + len(marker)
		brace := bytes.IndexByte(page[after:], '{')
		if brace < 0 {
			break
		}
		start := after + brace
		if end, ok := balancedObject(page, start); ok {
			candidate := page[start:end]
			if json.Valid(candidate) {
				return candidate, nil
			}
		}
		offset = after
	}
	return nil, errors.New("purchase page has no pageData JSON object")
}

// balancedObject returns the end offset of the JSON object starting at start.
func balancedObject(page []byte, start int) (int, bool) {
	if start >= len(page) || page[start] != '{' {
		return 0, false
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(page); i++ {
		c := page[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i + 1, true
			}
		}
	}
	return 0, false
}
