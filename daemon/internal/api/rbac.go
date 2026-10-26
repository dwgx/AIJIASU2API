package api

import (
	"bufio"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
)

// AdminRole is the authority of an interactive operator. Machine scoped keys
// retain their separate scope vocabulary; customer sessions never get one.
type AdminRole string

const (
	RoleViewer   AdminRole = "viewer"
	RoleOperator AdminRole = "operator"
	RoleAdmin    AdminRole = "admin"
)

func (r AdminRole) valid() bool { return r == RoleViewer || r == RoleOperator || r == RoleAdmin }

func (r AdminRole) permits(required AdminRole) bool {
	switch r {
	case RoleAdmin:
		return true
	case RoleOperator:
		return required == RoleViewer || required == RoleOperator
	case RoleViewer:
		return required == RoleViewer
	default:
		return false
	}
}

// OperatorSet is an immutable, hash-only snapshot of the operator token file.
// It is deliberately not persisted in the panel snapshot or included in a
// backup. Reloading requires a daemon restart; see deploy/production/README.md
// for the two-generation rotation procedure.
type OperatorSet struct {
	entries []operatorEntry
}

type operatorEntry struct {
	name string
	role AdminRole
	hash [sha256.Size]byte
}

// LoadOperatorSet reads username:role:sha256(token) records. The file must be
// a regular file with no group/other write or other access (0600, or 0640
// when its group is exactly the daemon group). Duplicate
// names are allowed only with the same role, which permits two-generation
// token rotation without changing identity or authority.
func LoadOperatorSet(path string) (*OperatorSet, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open admin operators: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat admin operators: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("admin operators must be a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o137 != 0 {
		return nil, fmt.Errorf("admin operators mode %04o is too permissive; use 0600 or root:ajs 0640", info.Mode().Perm())
	}
	if info.Size() > 128<<10 {
		return nil, errors.New("admin operators file exceeds 128 KiB")
	}
	set := &OperatorSet{}
	roles := make(map[string]AdminRole)
	scanner := bufio.NewScanner(io.LimitReader(f, 128<<10))
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(strings.TrimSuffix(scanner.Text(), "\r"))
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		parts := strings.Split(text, ":")
		if len(parts) != 3 {
			return nil, fmt.Errorf("admin operators line %d: want username:role:sha256", line)
		}
		name, role, digest := parts[0], AdminRole(parts[1]), parts[2]
		if sanitizeOperator(name) != name || !role.valid() {
			return nil, fmt.Errorf("admin operators line %d: invalid username or role", line)
		}
		if previous, ok := roles[name]; ok && previous != role {
			return nil, fmt.Errorf("admin operators line %d: operator %q has conflicting roles", line, name)
		}
		raw, err := hex.DecodeString(digest)
		if err != nil || len(raw) != sha256.Size {
			return nil, fmt.Errorf("admin operators line %d: token hash must be 64 lowercase/uppercase hex characters", line)
		}
		var hash [sha256.Size]byte
		copy(hash[:], raw)
		for _, existing := range set.entries {
			if subtle.ConstantTimeCompare(existing.hash[:], hash[:]) == 1 {
				return nil, fmt.Errorf("admin operators line %d: duplicate token hash", line)
			}
		}
		roles[name] = role
		set.entries = append(set.entries, operatorEntry{name: name, role: role, hash: hash})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read admin operators: %w", err)
	}
	if len(set.entries) == 0 {
		return nil, errors.New("admin operators file has no credentials")
	}
	return set, nil
}

func (s *OperatorSet) authenticate(token string) (operatorEntry, bool) {
	if s == nil || token == "" {
		return operatorEntry{}, false
	}
	candidate := sha256.Sum256([]byte(token))
	var matched operatorEntry
	found := 0
	// Do not return early: the number of entries is small and a match should
	// not become a per-token timing signal.
	for _, entry := range s.entries {
		if subtle.ConstantTimeCompare(entry.hash[:], candidate[:]) == 1 {
			matched, found = entry, 1
		}
	}
	return matched, found == 1
}

// routeRole classifies every admin route at registration. Reads default to
// viewer; exceptions containing security, tenant or financial data are admin.
// Unsafe routes default to admin, with explicit operator exceptions. An
// unregistered route therefore fails closed at admin rather than inheriting a
// broad "all writes" grant when a new handler is added.
func routeRole(method, pattern string) AdminRole {
	key := method + " " + pattern
	if role, ok := routeRoleOverrides[key]; ok {
		return role
	}
	if method == http.MethodGet {
		return RoleViewer
	}
	return RoleAdmin
}

var routeRoleOverrides = func() map[string]AdminRole {
	roles := make(map[string]AdminRole)
	admin := func(method string, patterns ...string) {
		for _, pattern := range patterns {
			roles[method+" "+pattern] = RoleAdmin
		}
	}
	operator := func(method string, patterns ...string) {
		for _, pattern := range patterns {
			roles[method+" "+pattern] = RoleOperator
		}
	}

	// Security-sensitive reads are not part of the general monitoring view.
	admin(http.MethodGet,
		"/v1/accounts/{id}/device",
		"/v1/scoped-keys", "/v1/scoped-keys/{id}",
		"/v1/webhooks", "/v1/webhooks/{id}/deliveries",
		"/v1/admin/plans", "/v1/admin/plans/{id}",
		"/v1/admin/orders", "/v1/admin/orders/{id}",
		"/v1/admin/tenants", "/v1/admin/tenants/invites",
	)

	// Routine traffic, node, lease, schedule and diagnostic operations.
	operator(http.MethodPost,
		"/v1/accounts/{id}/actions/login", "/v1/accounts/{id}/actions/logout",
		"/v1/accounts/{id}/actions/connect", "/v1/accounts/{id}/actions/disconnect",
		"/v1/accounts/{id}/actions/reconnect", "/v1/accounts/{id}/actions/select-node",
		"/v1/schedules", "/v1/schedules/preview", "/v1/schedules/{id}/actions/run",
		"/v1/lab/egress-check",
		"/v1/nodes/{id}/actions/drain", "/v1/nodes/{id}/actions/undrain", "/v1/nodes/{id}/actions/probe",
		"/v1/leases", "/v1/leases/{id}/actions/rotate",
		"/v1/requests/mutations", "/v1/reconcile",
	)
	operator(http.MethodPut, "/v1/accounts/{id}/selection")
	operator(http.MethodPatch, "/v1/schedules/{id}")
	operator(http.MethodDelete,
		"/v1/accounts/{id}/selection", "/v1/operations/{id}",
		"/v1/schedules/{id}", "/v1/leases/{id}",
	)
	return roles
}()
