package ajs

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// ListRequestsOptions filters GET /v1/requests. Limit is a pointer because the
// daemon defines 0 as "every retained row" while an omitted limit defaults to
// 200; nil keeps that default. A non-admin scoped key is served only its own
// UserID and a UserID naming another user answers 403 with reason
// scoped_key_restricted.
type ListRequestsOptions struct {
	UserID string
	Limit  *int
	Offset int
}

// ListRequests returns one page of recorded requests, newest first.
func (c *Client) ListRequests(ctx context.Context, opts ListRequestsOptions) ([]RequestLog, error) {
	q := url.Values{}
	setString(q, "user_id", opts.UserID)
	if opts.Limit != nil {
		q.Set("limit", strconv.Itoa(*opts.Limit))
	}
	setInt(q, "offset", opts.Offset)
	var out []RequestLog
	if _, err := c.do(ctx, http.MethodGet, "/v1/requests", q, nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}
