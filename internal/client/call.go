package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// request describes one API call.
type request struct {
	method string
	// path is relative to /v1 and already escaped; build it with
	// apiPath so IDs cannot inject segments or query strings.
	path  string
	query url.Values
	// body, when non-nil, is sent as JSON.
	body any
	// ok lists the statuses that count as success; 200 when empty.
	ok []int
}

// apiPath joins path segments, escaping each one.
func apiPath(segments ...string) string {
	escaped := make([]string, len(segments))
	for i, s := range segments {
		escaped[i] = url.PathEscape(s)
	}
	return "/" + strings.Join(escaped, "/")
}

// wsPath builds a path under a workspace.
func wsPath(workspaceID string, segments ...string) string {
	return apiPath(append([]string{"workspaces", workspaceID}, segments...)...)
}

// send issues r, encoding its body as JSON.
func (c *Client) send(ctx context.Context, r request) (*http.Response, error) {
	var body io.Reader
	var headers http.Header
	if r.body != nil {
		b, err := json.Marshal(r.body)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
		headers = http.Header{"Content-Type": []string{"application/json"}}
	}
	return c.sendReq(ctx, r.method, r.path, r.query, body, headers)
}

// do issues r and checks its status, discarding any response body. op
// names the call in errors ("failed to <op>: ...").
func (c *Client) do(ctx context.Context, op string, r request) error {
	rsp, err := c.send(ctx, r)
	if err != nil {
		return fmt.Errorf("failed to %s: %w", op, err)
	}
	defer rsp.Body.Close()
	if err := checkStatus(rsp, r.ok...); err != nil {
		return fmt.Errorf("failed to %s: %w", op, err)
	}
	return nil
}

// call issues r, checks its status and decodes the JSON response into
// T. An empty body leaves T at its zero value.
func call[T any](ctx context.Context, c *Client, op string, r request) (T, error) {
	var out T
	rsp, err := c.send(ctx, r)
	if err != nil {
		return out, fmt.Errorf("failed to %s: %w", op, err)
	}
	defer rsp.Body.Close()
	if err := checkStatus(rsp, r.ok...); err != nil {
		return out, fmt.Errorf("failed to %s: %w", op, err)
	}
	if err := json.NewDecoder(rsp.Body).Decode(&out); err != nil && !errors.Is(err, io.EOF) {
		return out, fmt.Errorf("failed to decode %s response: %w", op, err)
	}
	return out, nil
}

func get(path string, query ...url.Values) request {
	r := request{method: http.MethodGet, path: path}
	if len(query) > 0 {
		r.query = query[0]
	}
	return r
}

func post(path string, body any) request {
	return request{method: http.MethodPost, path: path, body: body}
}
