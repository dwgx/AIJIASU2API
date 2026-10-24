package api

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"ajsaccountd/internal/catalog"
	"ajsaccountd/internal/model"
)

// Server catalog query bounds. The default keeps the dashboard's first paint
// small; 500 is the largest page one request may ask for.
const (
	serverCatalogDefaultLimit = 50
	serverCatalogMaxLimit     = 500
)

// validServerCatalogSort reports whether key is an accepted ?sort= value.
// "order" is the default: the vendor's own display order with the stable id
// as tie-breaker.
func validServerCatalogSort(key string) bool {
	switch key {
	case "order", "title", "host", "status", "region":
		return true
	default:
		return false
	}
}

// serverCatalogQuery is the validated form of the catalog query parameters.
// selected is nil when the caller did not ask for a selection filter.
type serverCatalogQuery struct {
	q        string
	region   string
	status   string
	selected *bool
	province string
	city     string
	offset   int
	limit    int
	sort     string
}

// parseServerCatalogQuery validates q/region/status/selected/province/city/
// offset/limit/sort. A non-empty message means the request is invalid and the
// handler must answer 400 instead of silently widening the window.
func parseServerCatalogQuery(values url.Values) (serverCatalogQuery, string) {
	query := serverCatalogQuery{
		q:        strings.TrimSpace(values.Get("q")),
		region:   strings.ToLower(strings.TrimSpace(values.Get("region"))),
		status:   strings.ToLower(strings.TrimSpace(values.Get("status"))),
		province: strings.TrimSpace(values.Get("province")),
		city:     strings.TrimSpace(values.Get("city")),
		limit:    serverCatalogDefaultLimit,
		sort:     "order",
	}

	if raw := strings.TrimSpace(values.Get("offset")); raw != "" {
		offset, err := strconv.Atoi(raw)
		if err != nil || offset < 0 {
			return serverCatalogQuery{}, "offset must be a non-negative integer"
		}
		query.offset = offset
	}

	if raw := strings.TrimSpace(values.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > serverCatalogMaxLimit {
			return serverCatalogQuery{}, fmt.Sprintf("limit must be an integer between 1 and %d", serverCatalogMaxLimit)
		}
		query.limit = limit
	}

	if raw := strings.TrimSpace(values.Get("selected")); raw != "" {
		switch strings.ToLower(raw) {
		case "true":
			selected := true
			query.selected = &selected
		case "false":
			selected := false
			query.selected = &selected
		default:
			return serverCatalogQuery{}, "selected must be true or false"
		}
	}

	if raw := strings.TrimSpace(values.Get("sort")); raw != "" {
		key := strings.ToLower(raw)
		if !validServerCatalogSort(key) {
			return serverCatalogQuery{}, "sort must be one of title, host, status, region, order"
		}
		query.sort = key
	}

	return query, ""
}

// applyServerCatalogQuery filters, sorts and pages one catalog view. The
// runtime builds the catalog per request, so sorting its slice in place is
// safe and avoids a full copy of a 2,814-row catalog.
//
// The geography summary is computed over the q/region/status scope, before the
// province/city filter narrows the table, so the map keeps showing every
// province the current search matches. total counts every row matching all
// filters; regions summarizes those same rows, not just the current page.
// nextOffset is offset+len(Servers), equal to total on the last page.
func applyServerCatalogQuery(view model.ServersView, query serverCatalogQuery) model.ServersView {
	full := view.Servers
	scopeQuery := query
	scopeQuery.province = ""
	scopeQuery.city = ""
	scope := filterServerCatalog(full, scopeQuery)
	view.Geo = summarizeServerGeo(scope, full)

	servers := scope
	if query.province != "" || query.city != "" {
		servers = filterServerGeo(scope, query.province, query.city)
	}
	sortServerCatalog(servers, query.sort)

	total := len(servers)
	start := query.offset
	if start > total {
		start = total
	}
	end := start + query.limit
	if end > total {
		end = total
	}

	page := servers[start:end]
	if page == nil {
		page = []model.ServerView{}
	}
	view.Servers = page
	view.Total = total
	view.Offset = query.offset
	view.Limit = query.limit
	view.NextOffset = start + len(page)
	view.HasMore = view.NextOffset < total
	view.Regions = summarizeServerRegions(servers)
	return view
}

// filterServerGeo narrows rows to one province (or the unresolved `00`
// bucket) and optionally one city code. Resolution is the same exact match
// the dashboard uses, so the server and the client fallback agree.
func filterServerGeo(servers []model.ServerView, province, city string) []model.ServerView {
	out := make([]model.ServerView, 0, len(servers))
	for _, server := range servers {
		resolved, ok := catalog.ResolveTitle(server.Title)
		if province != "" {
			if province == catalog.UnknownProvince {
				if ok {
					continue
				}
			} else if !ok || resolved.ProvinceCode != province {
				continue
			}
		}
		if city != "" && (!ok || resolved.Code != city) {
			continue
		}
		out = append(out, server)
	}
	return out
}

// summarizeServerGeo builds the map aggregate. scope is the q/region/status
// filtered catalog; full is every retained row, so a running node stays named
// even when the current search or status filter hides it.
func summarizeServerGeo(scope, full []model.ServerView) *model.CatalogGeo {
	geo := &model.CatalogGeo{
		Total:     len(scope),
		Provinces: []model.CatalogGeoProvince{},
		Live:      []model.CatalogGeoRow{},
	}
	provinceIndex := make(map[string]int, 31)
	cityIndex := make(map[string]map[string]int, 31)
	for _, server := range scope {
		resolved, ok := catalog.ResolveTitle(server.Title)
		if !ok {
			geo.Unknown++
			continue
		}
		bucket, exists := provinceIndex[resolved.ProvinceCode]
		if !exists {
			bucket = len(geo.Provinces)
			provinceIndex[resolved.ProvinceCode] = bucket
			geo.Provinces = append(geo.Provinces, model.CatalogGeoProvince{
				Code:   resolved.ProvinceCode,
				Cities: []model.CatalogGeoCity{},
			})
		}
		province := &geo.Provinces[bucket]
		province.Count++
		if server.NodeState == model.NodeLive || server.NodeState == model.NodeDraining {
			province.Connected++
		}
		cities := cityIndex[resolved.ProvinceCode]
		if cities == nil {
			cities = map[string]int{}
			cityIndex[resolved.ProvinceCode] = cities
		}
		if city, seen := cities[resolved.Code]; seen {
			province.Cities[city].Count++
		} else {
			cities[resolved.Code] = len(province.Cities)
			province.Cities = append(province.Cities, model.CatalogGeoCity{
				Code:  resolved.Code,
				Label: catalog.ParseTitle(server.Title),
				Count: 1,
			})
		}
	}
	sort.Slice(geo.Provinces, func(i, j int) bool { return geo.Provinces[i].Code < geo.Provinces[j].Code })
	for index := range geo.Provinces {
		cities := geo.Provinces[index].Cities
		sort.Slice(cities, func(i, j int) bool { return cities[i].Label < cities[j].Label })
	}

	for _, server := range full {
		if server.NodeState != model.NodeLive {
			continue
		}
		resolved, _ := catalog.ResolveTitle(server.Title)
		row := model.CatalogGeoRow{
			ID:           server.ID,
			Title:        server.Title,
			NodeState:    server.NodeState,
			CityCode:     resolved.Code,
			ProvinceCode: resolved.ProvinceCode,
		}
		if geo.Current == nil {
			current := row
			geo.Current = &current
		}
		geo.Live = append(geo.Live, row)
	}
	return geo
}

// filterServerCatalog applies q, region, status and selected. It returns the
// input slice unchanged when no filter is set, so an unfiltered page costs no
// extra allocation.
func filterServerCatalog(servers []model.ServerView, query serverCatalogQuery) []model.ServerView {
	if query.q == "" && query.region == "" && query.status == "" && query.selected == nil {
		return servers
	}
	needle := strings.ToLower(query.q)
	out := make([]model.ServerView, 0, len(servers))
	for _, server := range servers {
		if needle != "" && !serverMatchesText(server, needle) {
			continue
		}
		if query.region != "" && serverRegion(server) != query.region {
			continue
		}
		if query.status != "" && strings.ToLower(strings.TrimSpace(server.Status)) != query.status {
			continue
		}
		if query.selected != nil && server.Selected != *query.selected {
			continue
		}
		out = append(out, server)
	}
	return out
}

// serverMatchesText is the case-insensitive substring search behind ?q=.
// id, title, host and region are the fields the catalog table shows.
func serverMatchesText(server model.ServerView, needle string) bool {
	return strings.Contains(strings.ToLower(server.ID), needle) ||
		strings.Contains(strings.ToLower(server.Title), needle) ||
		strings.Contains(strings.ToLower(server.Host), needle) ||
		strings.Contains(serverRegion(server), needle)
}

// serverRegion normalizes the catalog region: lowercase, with an empty value
// reading as "unknown", matching runtime.nodeRegion.
func serverRegion(server model.ServerView) string {
	if region := strings.ToLower(strings.TrimSpace(server.Region)); region != "" {
		return region
	}
	return "unknown"
}

func sortServerCatalog(servers []model.ServerView, key string) {
	sort.Slice(servers, func(i, j int) bool {
		if cmp := compareServerField(key, servers[i], servers[j]); cmp != 0 {
			return cmp < 0
		}
		return servers[i].ID < servers[j].ID
	})
}

// compareServerField compares the requested sort key. The id tie-breaker is
// applied by the caller, so equal keys still produce a deterministic order.
func compareServerField(key string, a, b model.ServerView) int {
	switch key {
	case "title":
		return strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
	case "host":
		return strings.Compare(strings.ToLower(a.Host), strings.ToLower(b.Host))
	case "status":
		return strings.Compare(strings.ToLower(a.Status), strings.ToLower(b.Status))
	case "region":
		return strings.Compare(serverRegion(a), serverRegion(b))
	default: // "order"
		return compareServerOrder(a.Order, b.Order)
	}
}

// compareServerOrder compares the vendor ord numerically when both values are
// integers, so "10" sorts after "2"; otherwise it falls back to lexicographic
// order. An empty ord sorts last.
func compareServerOrder(a, b string) int {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == b {
		return 0
	}
	if a == "" {
		return 1
	}
	if b == "" {
		return -1
	}
	ai, aerr := strconv.Atoi(a)
	bi, berr := strconv.Atoi(b)
	if aerr == nil && berr == nil {
		switch {
		case ai < bi:
			return -1
		case ai > bi:
			return 1
		default:
			return 0
		}
	}
	return strings.Compare(a, b)
}

// summarizeServerRegions counts the filtered rows per region, sorted by code
// so the summary is stable across pages and requests.
func summarizeServerRegions(servers []model.ServerView) []model.RegionCount {
	counts := make(map[string]int, 8)
	for _, server := range servers {
		counts[serverRegion(server)]++
	}
	codes := make([]string, 0, len(counts))
	for code := range counts {
		codes = append(codes, code)
	}
	sort.Strings(codes)

	out := make([]model.RegionCount, 0, len(codes))
	for _, code := range codes {
		out = append(out, model.RegionCount{Code: code, Count: counts[code]})
	}
	return out
}
