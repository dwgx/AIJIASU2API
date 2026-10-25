package ajs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
)

// ListLeasesOptions filters GET /v1/leases.
type ListLeasesOptions struct {
	UserID string
	Limit  int
	Offset int
}

// ListLeases returns one page of leases, optionally for one user.
func (c *Client) ListLeases(ctx context.Context, opts ListLeasesOptions) ([]Lease, error) {
	q := url.Values{}
	setString(q, "user_id", opts.UserID)
	setInt(q, "limit", opts.Limit)
	setInt(q, "offset", opts.Offset)
	var out []Lease
	if _, err := c.do(ctx, http.MethodGet, "/v1/leases", q, nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateLease creates an idempotent lease on a free node. When neither
// in.IdempotencyKey nor the option is set, the SDK generates a key and sends it
// as both the body field and the Idempotency-Key header, so a transient 5xx is
// retried safely. A replay returns Replayed=true with the original lease.
func (c *Client) CreateLease(ctx context.Context, in CreateLeaseInput, opts ...LeaseOption) (*CreateLeaseResult, error) {
	options := leaseOptions{}
	for _, opt := range opts {
		opt(&options)
	}
	key := strings.TrimSpace(in.IdempotencyKey)
	if options.idempotencyKey != "" {
		key = strings.TrimSpace(options.idempotencyKey)
	}
	if key == "" {
		generated, err := newIdempotencyKey()
		if err != nil {
			return nil, err
		}
		key = generated
	}
	in.IdempotencyKey = key
	headers := http.Header{}
	headers.Set("Idempotency-Key", key)
	var out CreateLeaseResult
	if _, err := c.do(ctx, http.MethodPost, "/v1/leases", nil, in, headers, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// LeaseOption customizes CreateLease.
type LeaseOption func(*leaseOptions)

type leaseOptions struct {
	idempotencyKey string
}

// WithIdempotencyKey sets the create key explicitly. It must agree with any
// key already present in the body; the option wins.
func WithIdempotencyKey(key string) LeaseOption {
	return func(o *leaseOptions) { o.idempotencyKey = key }
}

// RotateLease rotates a lease to a new egress IP and restarts its TTL.
func (c *Client) RotateLease(ctx context.Context, id string) (*Lease, error) {
	var out Lease
	if _, err := c.do(ctx, http.MethodPost, leasePath(id)+"/actions/rotate", nil, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ReleaseLease revokes a lease and frees its node. The daemon requires an
// explicit confirm:true, so this method always sends it.
func (c *Client) ReleaseLease(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodDelete, leasePath(id), nil, RevokeLeaseInput{Confirm: true}, nil, nil)
	return err
}

func leasePath(id string) string {
	return "/v1/leases/" + url.PathEscape(id)
}

func newIdempotencyKey() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return "sdk-" + hex.EncodeToString(buf[:]), nil
}
