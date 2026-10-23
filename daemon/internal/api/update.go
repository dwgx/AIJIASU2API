package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultUpdateAPIBase is the GitHub REST API root used when
// Config.UpdateAPIBase is empty.
const DefaultUpdateAPIBase = "https://api.github.com"

// updateCheckTimeout bounds a single GitHub release lookup.
const updateCheckTimeout = 10 * time.Second

// updateCheckResponse is the GET /v1/system/update contract.
type updateCheckResponse struct {
	Enabled         bool   `json:"enabled"`
	Current         string `json:"current"`
	Latest          string `json:"latest,omitempty"`
	UpdateAvailable bool   `json:"updateAvailable"`
	URL             string `json:"url,omitempty"`
	Name            string `json:"name,omitempty"`
	PublishedAt     string `json:"publishedAt,omitempty"`
	CheckedAt       string `json:"checkedAt,omitempty"`
}

// gitHubRelease is the subset of the GitHub releases/latest payload the
// check parses.
type gitHubRelease struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
}

// handleSystemUpdate serves GET /v1/system/update: a read-only GitHub
// Releases lookup against the configured repo. It never executes an
// update; the host runs deploy/update.sh. With Config.UpdateRepo empty
// the endpoint answers enabled:false and performs no network I/O.
func (s *Server) handleSystemUpdate(w http.ResponseWriter, r *http.Request) {
	if s.cfg.UpdateRepo == "" {
		writeJSON(w, http.StatusOK, updateCheckResponse{Enabled: false, Current: s.cfg.Version})
		return
	}
	base := s.cfg.UpdateAPIBase
	if base == "" {
		base = DefaultUpdateAPIBase
	}
	client := s.cfg.UpdateHTTP
	if client == nil {
		client = &http.Client{Timeout: updateCheckTimeout}
	}

	ctx, cancel := context.WithTimeout(r.Context(), updateCheckTimeout)
	defer cancel()
	url := fmt.Sprintf("%s/repos/%s/releases/latest", strings.TrimSuffix(base, "/"), s.cfg.UpdateRepo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, CodeUpstreamUnavailable, "update check request could not be built")
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ajs-accountd")

	resp, err := client.Do(req)
	if err != nil {
		s.logger.Warn("update_check_failed", "error", err)
		writeError(w, http.StatusBadGateway, CodeUpstreamUnavailable, "GitHub is unreachable")
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadGateway, CodeUpstreamUnavailable, "GitHub response could not be read")
		return
	}
	if resp.StatusCode != http.StatusOK {
		s.logger.Warn("update_check_status", "status", resp.StatusCode)
		writeError(w, http.StatusBadGateway, CodeUpstreamUnavailable, "GitHub did not answer with the latest release")
		return
	}
	var release gitHubRelease
	if err := json.Unmarshal(body, &release); err != nil {
		writeError(w, http.StatusBadGateway, CodeUpstreamUnavailable, "GitHub response could not be parsed")
		return
	}
	if release.TagName == "" {
		writeError(w, http.StatusBadGateway, CodeUpstreamUnavailable, "GitHub response has no tag name")
		return
	}
	writeJSON(w, http.StatusOK, updateCheckResponse{
		Enabled:         true,
		Current:         s.cfg.Version,
		Latest:          release.TagName,
		UpdateAvailable: newerVersion(release.TagName, s.cfg.Version, release.Draft, release.Prerelease),
		URL:             release.HTMLURL,
		Name:            release.Name,
		PublishedAt:     release.PublishedAt,
		CheckedAt:       time.Now().UTC().Format(time.RFC3339Nano),
	})
}

// newerVersion reports whether the GitHub tag is newer than the running
// version. The comparison is simple semver: an optional "v" prefix, then
// numeric dot segments compared left to right (missing segments are 0);
// anything non-numeric ends the comparison. A draft or pre-release tag is
// never "newer" unless the running version is itself a pre-release, so a
// stable deployment is never nagged into an unstable build.
func newerVersion(tag, current string, draft, prerelease bool) bool {
	if draft || prerelease {
		if !isPrerelease(current) {
			return false
		}
	}
	return compareVersion(tag, current) > 0
}

// isPrerelease reports whether the version carries a pre-release suffix
// (a "-" after the numeric part).
func isPrerelease(version string) bool {
	trimmed := strings.TrimPrefix(version, "v")
	dot := strings.IndexFunc(trimmed, func(r rune) bool { return r != '.' && (r < '0' || r > '9') })
	return dot >= 0 && trimmed[dot] == '-'
}

// compareVersion returns >0 when a is newer than b, <0 when older and 0
// when equal. Segments past the first non-numeric character are ignored.
func compareVersion(a, b string) int {
	as := splitNumeric(a)
	bs := splitNumeric(b)
	for i := 0; i < len(as) || i < len(bs); i++ {
		av, bv := 0, 0
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		if av != bv {
			if av > bv {
				return 1
			}
			return -1
		}
	}
	return 0
}

// splitNumeric returns the leading numeric dot segments of a version.
func splitNumeric(version string) []int {
	trimmed := strings.TrimPrefix(version, "v")
	end := strings.IndexFunc(trimmed, func(r rune) bool { return r != '.' && (r < '0' || r > '9') })
	if end >= 0 {
		trimmed = trimmed[:end]
	}
	if trimmed == "" {
		return nil
	}
	parts := strings.Split(strings.Trim(trimmed, "."), ".")
	values := make([]int, 0, len(parts))
	for _, part := range parts {
		value := 0
		for _, r := range part {
			value = value*10 + int(r-'0')
		}
		values = append(values, value)
	}
	return values
}
