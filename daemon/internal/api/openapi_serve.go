package api

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
)

// embeddedOpenAPISpec is the contract served by GET /v1/openapi.json. It is a
// byte-for-byte copy of docs/openapi.json; TestEmbeddedOpenAPISpecMatchesDocs
// fails when the two drift, because go:embed cannot reach outside the package.
//
//go:embed openapi.json
var embeddedOpenAPISpec []byte

// embeddedOpenAPIDigest is the SHA-256 of the embedded document, computed once
// at startup. It is the strong ETag, so an unchanged spec answers 304 without
// hashing the payload per request.
var embeddedOpenAPIDigest = func() string {
	sum := sha256.Sum256(embeddedOpenAPISpec)
	return hex.EncodeToString(sum[:])
}()

// handleOpenAPISpec serves the embedded OpenAPI 3.1 contract with a SHA-256
// ETag. The route sits behind the same bearer check as every other /v1 route.
func (s *Server) handleOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	etag := `"` + embeddedOpenAPIDigest + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(embeddedOpenAPISpec)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(embeddedOpenAPISpec)
}

// etagMatches implements the If-None-Match comparison: "*" matches any
// representation, weak validators compare by their opaque tag, and the header
// may carry a comma-separated list.
func etagMatches(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" {
			return true
		}
		if strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}
