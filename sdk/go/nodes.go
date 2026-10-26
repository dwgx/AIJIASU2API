package ajs

import (
	"context"
	"net/http"
	"net/url"
)

// ListNodesOptions filters GET /v1/nodes. Zero values are omitted. A non-admin
// scoped key only sees nodes of the accounts its grant names.
type ListNodesOptions struct {
	AccountID string
	State     NodeState
	Region    string
	Limit     int
	Offset    int
}

// ListNodes returns one page of proxy nodes.
func (c *Client) ListNodes(ctx context.Context, opts ListNodesOptions) ([]ProxyNode, error) {
	q := url.Values{}
	setString(q, "account_id", opts.AccountID)
	setString(q, "state", string(opts.State))
	setString(q, "region", opts.Region)
	setInt(q, "limit", opts.Limit)
	setInt(q, "offset", opts.Offset)
	var out []ProxyNode
	if _, err := c.do(ctx, http.MethodGet, "/v1/nodes", q, nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}
