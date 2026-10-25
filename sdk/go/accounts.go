package ajs

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// ListAccountsOptions filters GET /v1/accounts. Zero values are omitted.
type ListAccountsOptions struct {
	State  AccountState
	Limit  int
	Offset int
}

// ServersOptions filters GET /v1/accounts/{id}/servers.
type ServersOptions struct {
	Query    string
	Region   string
	Status   string
	Selected *bool
	Offset   int
	Limit    int
	Sort     string
}

// ListAccounts returns one page of accounts. Secrets are never returned.
func (c *Client) ListAccounts(ctx context.Context, opts ListAccountsOptions) ([]Account, error) {
	q := url.Values{}
	setString(q, "state", string(opts.State))
	setInt(q, "limit", opts.Limit)
	setInt(q, "offset", opts.Offset)
	var out []Account
	if _, err := c.do(ctx, http.MethodGet, "/v1/accounts", q, nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateAccount creates an account and starts its first login.
func (c *Client) CreateAccount(ctx context.Context, in CreateAccountInput) (*Account, error) {
	var out Account
	if _, err := c.do(ctx, http.MethodPost, "/v1/accounts", nil, in, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetAccount reads one account.
func (c *Client) GetAccount(ctx context.Context, id string) (*Account, error) {
	var out Account
	if _, err := c.do(ctx, http.MethodGet, accountPath(id), nil, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PatchAccount updates the entitlement fields of one account.
func (c *Client) PatchAccount(ctx context.Context, id string, patch AccountPatch) (*Account, error) {
	var out Account
	if _, err := c.do(ctx, http.MethodPatch, accountPath(id), nil, patch, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteAccount drains the account, deletes its record and keeps the audit.
func (c *Client) DeleteAccount(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodDelete, accountPath(id), nil, nil, nil, nil)
	return err
}

// LoginAccount forces a login.
func (c *Client) LoginAccount(ctx context.Context, id string) (*Account, error) {
	var out Account
	if _, err := c.do(ctx, http.MethodPost, accountPath(id)+"/actions/login", nil, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// LogoutAccount stops sessions, drains nodes and pauses the account.
func (c *Client) LogoutAccount(ctx context.Context, id string) (*Account, error) {
	var out Account
	if _, err := c.do(ctx, http.MethodPost, accountPath(id)+"/actions/logout", nil, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListAccountServers returns one page of the retained vendor catalog.
func (c *Client) ListAccountServers(ctx context.Context, id string, opts ServersOptions) (*ServersView, error) {
	q := url.Values{}
	setString(q, "q", opts.Query)
	setString(q, "region", opts.Region)
	setString(q, "status", opts.Status)
	if opts.Selected != nil {
		q.Set("selected", strconv.FormatBool(*opts.Selected))
	}
	setInt(q, "offset", opts.Offset)
	setInt(q, "limit", opts.Limit)
	setString(q, "sort", opts.Sort)
	var out ServersView
	if _, err := c.do(ctx, http.MethodGet, accountPath(id)+"/servers", q, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func accountPath(id string) string {
	return "/v1/accounts/" + url.PathEscape(id)
}

func setString(q url.Values, key, value string) {
	if value != "" {
		q.Set(key, value)
	}
}

func setInt(q url.Values, key string, value int) {
	if value != 0 {
		q.Set(key, strconv.Itoa(value))
	}
}
