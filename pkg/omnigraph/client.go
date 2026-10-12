// Package omnigraph is a minimal client for an Omnigraph server's stored
// queries: ploegd invokes the queries a deployment's bundle declares and
// never sends query text of its own.
package omnigraph

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Client reaches one Omnigraph server as one actor.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// Result is a stored read's rows and the graph commit they were read at.
type Result struct {
	Rows   []map[string]any `json:"rows"`
	Commit string           `json:"graph_commit_id"`
}

// StatusError is a refusal or failure the server answered with. A denied or
// unknown stored query answers 404, so a caller cannot probe the registry.
type StatusError struct {
	Status int
	Body   string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("omnigraph: HTTP %d: %s", e.Status, e.Body)
}

// Retryable reports whether trying the same request later may succeed.
func Retryable(err error) bool {
	var se *StatusError
	if errors.As(err, &se) {
		return se.Status == http.StatusTooManyRequests || se.Status >= 500
	}
	return err != nil
}

var queryName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Read invokes the stored read query name on graph.
func (c *Client) Read(ctx context.Context, graph, name string, params map[string]any) (Result, error) {
	var out Result
	err := c.invoke(ctx, graph, name, params, &out)
	return out, err
}

// Mutate invokes the stored mutation name on graph's main branch and returns
// the commit it published; empty when it matched nothing.
func (c *Client) Mutate(ctx context.Context, graph, name string, params map[string]any) (string, error) {
	var out struct {
		Commit *struct {
			ID string `json:"graph_commit_id"`
		} `json:"commit"`
	}
	if err := c.invoke(ctx, graph, name, params, &out); err != nil {
		return "", err
	}
	if out.Commit == nil {
		return "", nil
	}
	return out.Commit.ID, nil
}

func (c *Client) invoke(ctx context.Context, graph, name string, params map[string]any, out any) error {
	if !queryName.MatchString(name) {
		return fmt.Errorf("omnigraph: refusing stored query name %q", name)
	}
	if params == nil {
		params = map[string]any{}
	}
	body, err := json.Marshal(map[string]any{"params": params})
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(c.BaseURL, "/") + "/graphs/" + url.PathEscape(graph) + "/queries/" + name
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("omnigraph %s/%s: %w", graph, name, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return fmt.Errorf("omnigraph %s/%s: %w", graph, name, err)
	}
	if resp.StatusCode != http.StatusOK {
		return &StatusError{Status: resp.StatusCode, Body: strings.TrimSpace(string(data[:min(len(data), 300)]))}
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("omnigraph %s/%s: %w", graph, name, err)
	}
	return nil
}
