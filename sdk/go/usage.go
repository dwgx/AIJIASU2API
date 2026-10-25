package ajs

import (
	"context"
	"net/http"
	"net/url"
)

// UsageOptions filters GET /v1/usage. From and To accept an RFC 3339
// timestamp or a UTC date (YYYY-MM-DD); empty applies the daemon default
// (trailing 30 days).
type UsageOptions struct {
	From    string
	To      string
	Account string
	UserID  string
}

// GetUsage aggregates the request log over an inclusive UTC window.
func (c *Client) GetUsage(ctx context.Context, opts UsageOptions) (*UsageSummary, error) {
	q := url.Values{}
	setString(q, "from", opts.From)
	setString(q, "to", opts.To)
	setString(q, "account", opts.Account)
	setString(q, "userId", opts.UserID)
	var out UsageSummary
	if _, err := c.do(ctx, http.MethodGet, "/v1/usage", q, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
