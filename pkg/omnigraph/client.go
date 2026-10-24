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

// ErrPreconditionFailed is a conditional mutation the server refused with
// 412 because the branch moved past the commit it was conditioned on. The
// server wrote nothing: re-read, decide again, and only then write.
var ErrPreconditionFailed = errors.New("omnigraph: the graph moved past the commit the write was based on")

// Retryable reports whether trying the same request later may succeed.
func Retryable(err error) bool {
	var se *StatusError
	if errors.As(err, &se) {
		return se.Status == http.StatusTooManyRequests || se.Status >= 500
	}
	if errors.Is(err, ErrPreconditionFailed) {
		return false
	}
	return err != nil
}

// HTTPAPIContract is the Omnigraph HTTP contract every request declares. An
// Omnigraph 0.14 server refuses a request without it; older servers ignore it.
const HTTPAPIContract = "0.14"

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
	return c.mutate(ctx, graph, name, params, "")
}

// MutateIf invokes the stored mutation name only if graph's main branch is
// still at commit, the graph_commit_id of the read the write was decided on.
// It answers ErrPreconditionFailed when the branch moved, and never falls back
// to an unconditional write: a server without the conditional route answers
// 404 like an unknown query.
func (c *Client) MutateIf(ctx context.Context, graph, name string, params map[string]any, commit string) (string, error) {
	if commit == "" {
		return "", errors.New("omnigraph: a conditional write needs the commit it was based on")
	}
	return c.mutate(ctx, graph, name, params, commit)
}

func (c *Client) mutate(ctx context.Context, graph, name string, params map[string]any, ifCommit string) (string, error) {
	var out struct {
		Commit *struct {
			ID string `json:"graph_commit_id"`
		} `json:"commit"`
	}
	if err := c.invokeAt(ctx, graph, name, params, ifCommit, &out); err != nil {
		return "", err
	}
	if out.Commit == nil {
		return "", nil
	}
	return out.Commit.ID, nil
}

func (c *Client) invoke(ctx context.Context, graph, name string, params map[string]any, out any) error {
	return c.invokeAt(ctx, graph, name, params, "", out)
}

func (c *Client) invokeAt(ctx context.Context, graph, name string, params map[string]any, ifCommit string, out any) error {
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
	if ifCommit != "" {
		endpoint += "/if-graph-commit"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if ifCommit != "" {
		req.Header.Set("Omnigraph-If-Graph-Commit", ifCommit)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Omnigraph-Http-Api", HTTPAPIContract)
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
	if resp.StatusCode == http.StatusPreconditionFailed && ifCommit != "" {
		return fmt.Errorf("%w (%s/%s at %s)", ErrPreconditionFailed, graph, name, ifCommit)
	}
	if resp.StatusCode != http.StatusOK {
		return &StatusError{Status: resp.StatusCode, Body: strings.TrimSpace(string(data[:min(len(data), 300)]))}
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("omnigraph %s/%s: %w", graph, name, err)
	}
	return nil
}
