package litellm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// schemaStrictHandler is a fake LiteLLM proxy that enforces real request
// shapes: unknown query params are rejected, return_full_object controls
// the response shape, and key_alias is an exact-match filter.
//
//nolint:unparam
func schemaStrictHandler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/key/generate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req MintRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.KeyAlias == "" {
			http.Error(w, "key_alias is required", http.StatusBadRequest)
			return
		}
		for _, m := range req.Models {
			if strings.Contains(m, "/") {
				http.Error(w, "model scope contains '/' — strip proxy prefix first", http.StatusBadRequest)
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"key": "sk-" + req.KeyAlias})
	})

	// In-memory key store for the fake so ListKeys reflects deletions.
	type fakeKey struct {
		Token    string `json:"token"`
		KeyAlias string `json:"key_alias"`
	}
	var storeMu int // dummy; real code uses a mutex but tests are sequential
	keyStore := map[string]fakeKey{}

	mux.HandleFunc("/key/delete", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Keys []string `json:"keys"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = storeMu
		var deleted int
		for _, tok := range req.Keys {
			if _, ok := keyStore[tok]; ok {
				delete(keyStore, tok)
				deleted++
			}
		}
		if deleted == 0 {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"No keys found"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"deleted_keys": req.Keys, "deleted_count": deleted})
	})

	mux.HandleFunc("/key/list", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query()

		// Schema-strict: reject unknown params.
		for param := range q {
			if param != "return_full_object" && param != "size" && param != "page" {
				http.Error(w, fmt.Sprintf("unknown query param: %s", param), http.StatusBadRequest)
				return
			}
		}

		fullObj := q.Get("return_full_object") == "true"

		// Without return_full_object, keys is a string slice (plain hashes).
		if !fullObj {
			tokens := []string{} // real proxy emits [], never null
			for _, k := range keyStore {
				tokens = append(tokens, k.Token)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"keys":         tokens,
				"total_count":  len(tokens),
				"total_pages":  1,
				"current_page": 1,
			})
			return
		}

		// With return_full_object=true, keys are objects with token/key_alias.
		keys := []fakeKey{} // real proxy emits [], never null
		for _, k := range keyStore {
			keys = append(keys, k)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys":         keys,
			"total_count":  len(keys),
			"total_pages":  1,
			"current_page": 1,
		})
	})

	// Helper endpoint to seed keys into the store.
	mux.HandleFunc("/__seed", func(w http.ResponseWriter, r *http.Request) {
		var seeds []fakeKey
		if err := json.NewDecoder(r.Body).Decode(&seeds); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for _, s := range seeds {
			keyStore[s.Token] = s
		}
		w.WriteHeader(http.StatusOK)
	})

	return mux
}

func TestAlias_OK(t *testing.T) {
	got := Alias("aabbccddee0011223344556677889900")
	want := "ploeg-aabbccddee00"
	if got != want {
		t.Fatalf("Alias() = %q, want %q", got, want)
	}
}

func TestAlias_ShortTokenReturnsEmpty(t *testing.T) {
	if got := Alias("short"); got != "" {
		t.Fatalf("Alias('short') = %q, want empty", got)
	}
}

func TestAlias_EmptyTokenReturnsEmpty(t *testing.T) {
	if got := Alias(""); got != "" {
		t.Errorf("Alias('') = %q, want empty", got)
	}
}

func TestListKeys_MissingFullObjectReturnsStrings(t *testing.T) {
	srv := httptest.NewServer(schemaStrictHandler())
	defer srv.Close()
	cli := NewClient(srv.URL, "test-key")

	// Without return_full_object=true, the fake returns string tokens.
	// The client always sets return_full_object=true, so this just tests
	// that we handle the case gracefully (no panic on decode mismatch).
	keys, err := cli.ListKeys(context.Background(), "ploeg-")
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("expected 0 keys on empty store, got %d", len(keys))
	}
}

func TestListKeys_PrefixFilter(t *testing.T) {
	srv := httptest.NewServer(schemaStrictHandler())
	defer srv.Close()
	cli := NewClient(srv.URL, "test-key")

	ctx := context.Background()

	// Seed keys via the helper endpoint.
	seeds := []map[string]string{
		{"token": "tok-ploeg-aaa", "key_alias": "ploeg-aabbccddee00"},
		{"token": "tok-ploeg-bbb", "key_alias": "ploeg-bbccddeeff11"},
		{"token": "tok-other", "key_alias": "other-key"},
	}
	seedBody, _ := json.Marshal(seeds)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/__seed", strings.NewReader(string(seedBody)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("seed request: %v", err)
	}
	resp.Body.Close()

	keys, err := cli.ListKeys(ctx, "ploeg-")
	if err != nil {
		t.Fatalf("ListKeys(ploeg-): %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys matching ploeg-, got %d", len(keys))
	}
	for _, k := range keys {
		if !strings.HasPrefix(k.KeyAlias, "ploeg-") {
			t.Errorf("unexpected key alias %q", k.KeyAlias)
		}
	}
}

func TestDeleteKeys_Idempotent404(t *testing.T) {
	srv := httptest.NewServer(schemaStrictHandler())
	defer srv.Close()
	cli := NewClient(srv.URL, "test-key")

	ctx := context.Background()

	// Delete a key that doesn't exist — should not error (idempotent).
	if err := cli.DeleteKeys(ctx, []string{"nonexistent-token"}); err != nil {
		t.Fatalf("DeleteKeys on missing key: %v", err)
	}
}

func TestDeleteKeys_EmptyBatch(t *testing.T) {
	cli := NewClient("http://example.com", "test-key")
	if err := cli.DeleteKeys(context.Background(), nil); err != nil {
		t.Fatalf("DeleteKeys(nil): %v", err)
	}
	if err := cli.DeleteKeys(context.Background(), []string{}); err != nil {
		t.Fatalf("DeleteKeys([]): %v", err)
	}
}

func TestMintRevoke_WithFakeServer(t *testing.T) {
	srv := httptest.NewServer(schemaStrictHandler())
	defer srv.Close()

	cli := NewClient(srv.URL, "test-master-key")

	ctx := context.Background()
	key, err := cli.Mint(ctx, MintRequest{
		KeyAlias:  "ploeg-test",
		MaxBudget: 100,
		Models:    []string{"gpt-4"},
	})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if key != "sk-ploeg-test" {
		t.Fatalf("got key %q, want sk-ploeg-test", key)
	}

	if err := cli.Revoke(ctx, key); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
}

func TestMintRevoke_ErrorResponses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer srv.Close()

	cli := NewClient(srv.URL, "bad-key")
	ctx := context.Background()

	_, err := cli.Mint(ctx, MintRequest{KeyAlias: "test", MaxBudget: 10})
	if err == nil {
		t.Fatal("expected error from Mint, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 401") {
		t.Errorf("error should mention HTTP status: %v", err)
	}

	// Revoke is now DeleteKeys under the hood — same error path.
	if err := cli.Revoke(ctx, "sk-test"); err == nil {
		t.Fatal("expected error from Revoke, got nil")
	}
}

func TestMintRequest_EmptyModels(t *testing.T) {
	srv := httptest.NewServer(schemaStrictHandler())
	defer srv.Close()

	cli := NewClient(srv.URL, "test-key")

	key, err := cli.Mint(context.Background(), MintRequest{
		KeyAlias: "ploeg-test", MaxBudget: 50,
	})
	if err != nil {
		t.Fatalf("Mint (nil models): %v", err)
	}
	if key != "sk-ploeg-test" {
		t.Errorf("got %q, want sk-ploeg-test", key)
	}

	key, err = cli.Mint(context.Background(), MintRequest{
		KeyAlias: "ploeg-test", MaxBudget: 50, Models: []string{},
	})
	if err != nil {
		t.Fatalf("Mint (empty models): %v", err)
	}
	if key != "sk-ploeg-test" {
		t.Errorf("got %q, want sk-ploeg-test", key)
	}
}

func TestMint_EmptyKeyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"key": ""})
	}))
	defer srv.Close()

	cli := NewClient(srv.URL, "test-key")
	_, err := cli.Mint(context.Background(), MintRequest{KeyAlias: "test"})
	if err == nil {
		t.Fatal("expected error for empty key, got nil")
	}
}

func TestMint_NetworkError(t *testing.T) {
	cli := NewClient("http://127.0.0.1:1", "test-key")
	ctx := context.Background()
	_, err := cli.Mint(ctx, MintRequest{KeyAlias: "test"})
	if err == nil {
		t.Fatal("expected error for connection refused, got nil")
	}
}

func TestListKeys_RejectsUnknownParams(t *testing.T) {
	// Verify the schema-strict handler rejects unknown query params.
	srv := httptest.NewServer(schemaStrictHandler())
	defer srv.Close()

	// Direct call with an unknown param.
	u, _ := url.Parse(srv.URL + "/key/list?return_full_object=true&size=100&page=1&unknown=true")
	req, _ := http.NewRequest(http.MethodGet, u.String(), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for unknown param, got %d", resp.StatusCode)
	}
}

func TestListKeys_WithoutFullObject(t *testing.T) {
	// Verify the fake returns plain strings when return_full_object is
	// missing — the client always sends it, but the fake must be strict.
	srv := httptest.NewServer(schemaStrictHandler())
	defer srv.Close()

	u, _ := url.Parse(srv.URL + "/key/list")
	req, _ := http.NewRequest(http.MethodGet, u.String(), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	keys, ok := raw["keys"].([]any)
	if !ok {
		t.Fatal("keys is not an array")
	}
	if len(keys) > 0 {
		// Without full objects, entries are strings, not maps.
		if _, isStr := keys[0].(string); !isStr {
			t.Errorf("expected string entries without full objects, got %T", keys[0])
		}
	}
}

func TestMissingGatewaySpendIsNotZero(t *testing.T) {
	for _, body := range []string{`{}`, `{"info":{}}`, `{"info":{"spend":null}}`, `{"info":{"spend":-1}}`} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer srv.Close()
			if _, err := NewClient(srv.URL, "fixture-master").KeySpend(context.Background(), "fixture-key-id"); err == nil {
				t.Fatal("missing or invalid gateway spend accepted as zero")
			}
		})
	}
}

func TestGatewayErrorsNeverEchoCredentialOrResponseBody(t *testing.T) {
	const canary = "fixture-secret-canary"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(canary))
	}))
	defer srv.Close()
	cli := NewClient(srv.URL, canary)
	ctx := context.Background()
	_, mintErr := cli.Mint(ctx, MintRequest{KeyAlias: "fixture", MaxBudget: 1})
	_, spendErr := cli.KeySpend(ctx, canary)
	_, listErr := cli.ListKeys(ctx, "fixture")
	for _, err := range []error{mintErr, spendErr, listErr, cli.BlockKey(ctx, canary), cli.DeleteKeys(ctx, []string{canary})} {
		if err == nil || strings.Contains(err.Error(), canary) {
			t.Fatalf("unsafe gateway error: %v", err)
		}
	}
	invalid := NewClient("://"+canary, "fixture-master")
	if _, err := invalid.KeySpend(ctx, canary); err == nil || strings.Contains(err.Error(), canary) {
		t.Fatalf("invalid endpoint disclosed credential: %v", err)
	}
	if _, _, err := cli.SpendLogTotal(ctx, canary); err == nil || strings.Contains(err.Error(), canary) {
		t.Fatalf("unsafe spend logs error: %v", err)
	}
}

func TestSpendLogTotalSumsOnlyTheRequestedKey(t *testing.T) {
	var query string
	body := `[{"api_key":"fixture-key-id","spend":0.1},{"api_key":"fixture-key-id","spend":0.25}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		if r.URL.Path != "/spend/logs" || r.Header.Get("Authorization") != "Bearer fixture-master" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	cli := NewClient(srv.URL, "fixture-master")
	total, entries, err := cli.SpendLogTotal(context.Background(), "fixture-key-id")
	if err != nil || entries != 2 || total < 0.3499 || total > 0.3501 || query != "api_key=fixture-key-id" {
		t.Fatalf("total=%v entries=%d err=%v query=%q", total, entries, err, query)
	}
	for _, invalid := range []string{
		`{}`,
		`[{"api_key":"other-key","spend":0.1}]`,
		`[{"api_key":"fixture-key-id"}]`,
		`[{"api_key":"fixture-key-id","spend":-1}]`,
		`[{"api_key":"fixture-key-id","spend":0.1}`,
	} {
		body = invalid
		if _, _, err := cli.SpendLogTotal(context.Background(), "fixture-key-id"); err == nil {
			t.Fatalf("accepted spend logs %s", invalid)
		}
	}
}

func TestSpendLogsAggregatesTokensAndModels(t *testing.T) {
	body := `[{"api_key":"fixture-key-id","spend":0.1,"model":"deepseek-chat","prompt_tokens":1200,"completion_tokens":300},` +
		`{"api_key":"fixture-key-id","spend":0.25,"model":"claude-sonnet","prompt_tokens":800,"completion_tokens":150},` +
		`{"api_key":"fixture-key-id","spend":0,"model":"deepseek-chat"},` +
		`{"api_key":"fixture-key-id","spend":0.05,"model":" ","prompt_tokens":-4,"completion_tokens":null}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	got, err := NewClient(srv.URL, "fixture-master").SpendLogs(context.Background(), "fixture-key-id")
	if err != nil {
		t.Fatal(err)
	}
	if got.Entries != 4 || got.USD < 0.3999 || got.USD > 0.4001 || got.PromptTokens != 2000 || got.CompletionTokens != 450 {
		t.Fatalf("summary=%+v", got)
	}
	if strings.Join(got.Models, ",") != "claude-sonnet,deepseek-chat" {
		t.Fatalf("models=%v, want sorted and unique", got.Models)
	}
	want := []ModelUsage{
		{Model: "claude-sonnet", USD: 0.25, Entries: 1, PromptTokens: 800, CompletionTokens: 150},
		{Model: "deepseek-chat", USD: 0.1, Entries: 2, PromptTokens: 1200, CompletionTokens: 300},
	}
	if len(got.ByModel) != len(want) {
		t.Fatalf("byModel=%+v, want one share per named model", got.ByModel)
	}
	for i, w := range want {
		g := got.ByModel[i]
		if g.Model != w.Model || g.Entries != w.Entries || g.PromptTokens != w.PromptTokens || g.CompletionTokens != w.CompletionTokens || g.USD < w.USD-1e-9 || g.USD > w.USD+1e-9 {
			t.Fatalf("byModel[%d]=%+v, want %+v", i, g, w)
		}
	}
}

// ADR-0078: without a team the /key/generate body is exactly what it was —
// no team_id and no object_permission, so the key gets no MCP tools. With a
// team it carries team_id and object_permission.mcp_access_groups, the pair
// LiteLLM checks against the team.
func TestMint_TeamAndMCPAccessGroupsOnTheWire(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		bodies = append(bodies, body)
		_ = json.NewEncoder(w).Encode(map[string]string{"key": "sk-test"})
	}))
	defer srv.Close()
	cli := NewClient(srv.URL, "test-key")
	ctx := context.Background()
	if _, err := cli.Mint(ctx, MintRequest{KeyType: "llm_api", KeyAlias: "ploeg-a", MaxBudget: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Mint(ctx, MintRequest{KeyType: "llm_api", KeyAlias: "ploeg-b", MaxBudget: 1, TeamID: "orders-team-id",
		ObjectPermission: &ObjectPermission{MCPAccessGroups: []string{"observability-read-orders"}}}); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 {
		t.Fatalf("got %d mint bodies", len(bodies))
	}
	for _, field := range []string{"team_id", "object_permission"} {
		if _, ok := bodies[0][field]; ok {
			t.Errorf("a mint without a team sent %s: %v", field, bodies[0])
		}
	}
	if bodies[1]["team_id"] != "orders-team-id" {
		t.Errorf("team_id = %v, want orders-team-id", bodies[1]["team_id"])
	}
	perm, _ := bodies[1]["object_permission"].(map[string]any)
	groups, _ := perm["mcp_access_groups"].([]any)
	if len(groups) != 1 || groups[0] != "observability-read-orders" {
		t.Errorf("object_permission = %v, want mcp_access_groups [observability-read-orders]", bodies[1]["object_permission"])
	}
	if bodies[1]["key_type"] != "llm_api" {
		t.Errorf("key_type = %v, want llm_api", bodies[1]["key_type"])
	}
}

func TestSpendLogsByAlias_PagesMatchesTheExactAliasAndRefusesWhatItCannotTrust(t *testing.T) {
	alias := "ploeg-0123456789ab"
	var pages []string
	capped, badSpend := false, false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/spend/logs/ui" || q.Get("key_alias") != alias || r.Header.Get("Authorization") != "Bearer master" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if _, err := time.Parse("2006-01-02 15:04:05", q.Get("end_date")); err != nil || q.Get("start_date") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		pages = append(pages, q.Get("page"))
		row := func(a string, spend any) map[string]any {
			return map[string]any{"api_key": "k-" + a, "spend": spend, "model": "coding", "prompt_tokens": 4, "completion_tokens": 1, "metadata": map[string]any{"user_api_key_alias": a}}
		}
		var data []map[string]any
		if q.Get("page") == "1" {
			data = []map[string]any{row(alias, 0.25), row(alias+"0", 5.0)}
		} else {
			spend := any(0.5)
			if badSpend {
				spend = nil
			}
			data = []map[string]any{row(alias, spend)}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "total": 3, "page": q.Get("page"), "page_size": 1000, "total_pages": 2, "total_is_capped": capped})
	}))
	defer srv.Close()
	cli := NewClient(srv.URL, "master")
	ctx := context.Background()

	got, keys, err := cli.SpendLogsByAlias(ctx, alias, time.Now())
	if err != nil || got.USD != 0.75 || got.Entries != 2 || keys != 1 || got.PromptTokens != 8 || len(pages) != 2 {
		t.Fatalf("summary=%+v keys=%d pages=%v err=%v; want two pages and only the exact alias", got, keys, pages, err)
	}
	badSpend = true
	if _, _, err := cli.SpendLogsByAlias(ctx, alias, time.Now()); err == nil {
		t.Fatal("an entry without a spend was summed")
	}
	badSpend, capped = false, true
	if _, _, err := cli.SpendLogsByAlias(ctx, alias, time.Now()); err == nil || !strings.Contains(err.Error(), "cap") {
		t.Fatalf("a capped result was trusted: %v", err)
	}
	if _, _, err := cli.SpendLogsByAlias(ctx, "", time.Now()); err == nil {
		t.Fatal("an empty alias was read")
	}
}
