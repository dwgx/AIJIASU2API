// Package catalog holds the daemon's offline geography: the committed
// dashboard city asset reduced to the exact label -> city/province lookup the
// catalog filters and map aggregates need.
package catalog

import "strings"

// UnknownProvince is the code the dashboard uses for an unresolved title. It
// is also the ?province= value that selects that bucket.
const UnknownProvince = "00"

// City is one resolved vendor label. Code is the GB/T 2260 city code and
// ProvinceCode its two-digit province code.
type City struct {
	Code         string
	ProvinceCode string
}

// ParseTitle strips the vendor's `#<n>` suffix: `镇江 #1` -> `镇江`. A title
// without the suffix is used whole, matching the dashboard's parser.
func ParseTitle(title string) string {
	trimmed := strings.TrimSpace(title)
	index := strings.LastIndex(trimmed, "#")
	if index < 0 {
		return trimmed
	}
	suffix := strings.TrimSpace(trimmed[index+1:])
	if suffix == "" {
		return trimmed
	}
	for _, char := range suffix {
		if char < '0' || char > '9' {
			return trimmed
		}
	}
	return strings.TrimSpace(trimmed[:index])
}

// ResolveTitle resolves an exact vendor city label. There is deliberately no
// prefix or fuzzy match: `海南` must not resolve to 青海's 海南藏族自治州.
func ResolveTitle(title string) (City, bool) {
	city, ok := cityIndex[ParseTitle(title)]
	return city, ok
}
