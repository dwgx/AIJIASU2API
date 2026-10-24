package catalog

import "testing"

func TestParseTitle(t *testing.T) {
	cases := map[string]string{
		"镇江 #1":       "镇江",
		"  哈尔滨 #510 ": "哈尔滨",
		"Vendor Edge": "Vendor Edge",
		"海南 #12":      "海南",
		"#1":          "",
	}
	for input, want := range cases {
		if got := ParseTitle(input); got != want {
			t.Errorf("ParseTitle(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestResolveTitleIsExact(t *testing.T) {
	cases := []struct {
		title        string
		code         string
		provinceCode string
		ok           bool
	}{
		// Short vendor labels resolve through the generated aliases.
		{"镇江 #1", "321100", "32", true},
		{"湘西 #1", "433100", "43", true},
		{"西双版纳 #1", "532800", "53", true},
		{"延吉 #1", "222400", "22", true},
		{"济南 - 莱芜 #1", "370100", "37", true},
		// Full DataV names stay exact.
		{"海南藏族自治州 #1", "632500", "63", true},
		// A province short name is not a city alias for a different province:
		// 海南 must never resolve to 青海's 海南藏族自治州.
		{"海南 #1", "", "", false},
		{"Atlantis #1", "", "", false},
		{"吉林 #1", "220200", "22", true},
	}
	for _, tc := range cases {
		city, ok := ResolveTitle(tc.title)
		if ok != tc.ok {
			t.Errorf("ResolveTitle(%q) ok = %v, want %v", tc.title, ok, tc.ok)
			continue
		}
		if city.Code != tc.code || city.ProvinceCode != tc.provinceCode {
			t.Errorf("ResolveTitle(%q) = %+v, want %s/%s", tc.title, city, tc.code, tc.provinceCode)
		}
	}
}
