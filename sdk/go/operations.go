package ajs

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ListOperationsOptions filters GET /v1/operations.
type ListOperationsOptions struct {
	Account string
	State   OperationState
	Type    string
	Limit   int
}

// WaitOptions controls operation polling.
type WaitOptions struct {
	// Interval is the first poll delay. Zero uses the server's Retry-After or 1s.
	Interval time.Duration
	// MaxInterval caps the growth of the poll delay.
	MaxInterval time.Duration
	// Timeout bounds the whole wait. Zero waits until ctx is done.
	Timeout time.Duration
}

func (o WaitOptions) normalized() WaitOptions {
	if o.Interval <= 0 {
		o.Interval = time.Second
	}
	if o.MaxInterval <= 0 {
		o.MaxInterval = 5 * time.Second
	}
	if o.MaxInterval < o.Interval {
		o.MaxInterval = o.Interval
	}
	return o
}

// SelectNode selects the account's live node. It posts the declarative
// select-node request, then polls the operation to a terminal state when the
// daemon answers 202. An already-current target (200) returns Changed=false
// without an operation. A failed or canceled operation is returned as
// *OperationFailure; a local poll timeout as *OperationTimeoutError.
func (c *Client) SelectNode(ctx context.Context, accountID string, req SelectNodeRequest, wait ...WaitOptions) (*SelectNodeResult, error) {
	path := accountPath(accountID) + "/actions/select-node"
	waitOpts := WaitOptions{}
	if len(wait) > 0 {
		waitOpts = wait[0]
	}
	resp, err := c.do(ctx, http.MethodPost, path, nil, req, nil, nil)
	if err != nil {
		// wait:true can answer 504 after the operation was created; the durable
		// id in details lets this client keep waiting instead of losing it.
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusGatewayTimeout {
			if id := apiErr.Details["operationId"]; id != "" {
				return c.finishSelectNode(ctx, id, waitOpts)
			}
		}
		return nil, err
	}
	switch resp.status {
	case http.StatusAccepted:
		var operation Operation
		if err := decodeBody(resp.body, &operation); err != nil {
			return nil, err
		}
		id := operationIDFromLocation(resp.header.Get("Location"))
		if id == "" {
			id = operation.ID
		}
		if id == "" {
			return nil, errors.New("ajs: select-node accepted without an operation id")
		}
		if waitOpts.Interval == 0 {
			waitOpts.Interval = parseRetryAfter(resp.header.Get("Retry-After"), time.Now())
		}
		return c.finishSelectNode(ctx, id, waitOpts)
	case http.StatusOK:
		var result SelectNodeResult
		// The 200 body is a SelectNodeResult, not an Operation.
		if err := decodeBody(resp.body, &result); err != nil {
			return nil, err
		}
		return &result, nil
	default:
		return nil, errors.New("ajs: unexpected select-node status " + strconv.Itoa(resp.status))
	}
}

// finishSelectNode polls one operation and returns its result.
func (c *Client) finishSelectNode(ctx context.Context, id string, opts WaitOptions) (*SelectNodeResult, error) {
	operation, err := c.WaitOperation(ctx, id, opts)
	if err != nil {
		return nil, err
	}
	switch operation.State {
	case OperationSucceeded:
		if operation.Result == nil {
			return nil, errors.New("ajs: operation " + id + " succeeded without a result")
		}
		return operation.Result, nil
	case OperationCanceled:
		return nil, &OperationFailure{
			OperationID: id, State: operation.State,
			Code: "OPERATION_CANCELED", Message: "operation was canceled",
		}
	default:
		failure := &OperationFailure{
			OperationID: id, State: operation.State,
			Code: "OPERATION_FAILED", Message: "operation failed",
		}
		if operation.Error != nil {
			failure.Code = operation.Error.Code
			failure.Message = operation.Error.Message
			failure.Details = operation.Error.Details
		}
		return nil, failure
	}
}

// WaitOperation polls GET /v1/operations/{id} until the record is terminal and
// returns it. A failed operation is returned without an error: inspect State
// and Error. Only a transport failure, a local timeout or ctx cancellation is
// an error.
func (c *Client) WaitOperation(ctx context.Context, id string, opts WaitOptions) (*Operation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	opts = opts.normalized()
	waitCtx := ctx
	cancel := func() {}
	if opts.Timeout > 0 {
		waitCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
	}
	defer cancel()
	interval := opts.Interval
	for {
		var operation Operation
		if _, err := c.do(waitCtx, http.MethodGet, operationPath(id), nil, nil, nil, &operation); err != nil {
			if waitCtx.Err() != nil {
				return nil, pollError(ctx, waitCtx, id, opts.Timeout)
			}
			return nil, err
		}
		if operation.State.Terminal() {
			return &operation, nil
		}
		if err := c.wait(waitCtx, interval); err != nil {
			return nil, pollError(ctx, waitCtx, id, opts.Timeout)
		}
		interval *= 2
		if interval > opts.MaxInterval {
			interval = opts.MaxInterval
		}
	}
}

func pollError(parent, waitCtx context.Context, id string, timeout time.Duration) error {
	if parent.Err() != nil {
		return parent.Err()
	}
	if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
		return &OperationTimeoutError{OperationID: id, Timeout: timeout}
	}
	return waitCtx.Err()
}

// ListOperations lists async operations newest first.
func (c *Client) ListOperations(ctx context.Context, opts ListOperationsOptions) ([]Operation, error) {
	q := url.Values{}
	setString(q, "account", opts.Account)
	setString(q, "state", string(opts.State))
	setString(q, "type", opts.Type)
	setInt(q, "limit", opts.Limit)
	var out []Operation
	if _, err := c.do(ctx, http.MethodGet, "/v1/operations", q, nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetOperation reads one operation with its steps, result and error.
func (c *Client) GetOperation(ctx context.Context, id string) (*Operation, error) {
	var out Operation
	if _, err := c.do(ctx, http.MethodGet, operationPath(id), nil, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CancelOperation cancels a queued operation before dispatch.
func (c *Client) CancelOperation(ctx context.Context, id string) (*Operation, error) {
	var out Operation
	if _, err := c.do(ctx, http.MethodDelete, operationPath(id), nil, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// operationIDFromLocation takes the last path segment of a Location header,
// which may be absolute or relative.
func operationIDFromLocation(location string) string {
	location = strings.TrimRight(location, "/")
	if i := strings.LastIndex(location, "/"); i >= 0 {
		return location[i+1:]
	}
	return location
}

func operationPath(id string) string {
	return "/v1/operations/" + url.PathEscape(id)
}
