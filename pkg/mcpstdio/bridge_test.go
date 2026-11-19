package mcpstdio

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("MCPSTDIO_CHILD") == "1" {
		child()
		return
	}
	os.Exit(m.Run())
}

func child() {
	calls := 0
	in := bufio.NewScanner(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	for in.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal(in.Bytes(), &m)
		if len(m.ID) == 0 {
			continue
		}
		calls++
		if m.Method == "probe" {
			cwd, _ := os.Getwd()
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"home": os.Getenv("HOME"), "tmpdir": os.Getenv("TMPDIR"), "cwd": cwd, "leak": os.Getenv("MCPSTDIO_LEAK")}})
			continue
		}
		if m.Method == "tools/call" {
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": 900, "method": "sampling/createMessage", "params": map[string]any{}})
			if !in.Scan() || !strings.Contains(in.Text(), `"error"`) {
				return
			}
		}
		_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"calls": calls, "pid": os.Getpid()}})
	}
}

func bridge(t *testing.T, sessions int) *httptest.Server {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b := &Bridge{Command: []string{exe}, Env: append(os.Environ(), "MCPSTDIO_CHILD=1"), Sessions: sessions}
	srv := httptest.NewServer(b)
	t.Cleanup(func() {
		srv.Close()
		b.ReapIdle(time.Now().Add(time.Hour))
	})
	return srv
}

func post(t *testing.T, url, session, body string) (*http.Response, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp, out
}

const initialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`

func TestEachSessionGetsItsOwnServerProcess(t *testing.T) {
	srv := bridge(t, 4)
	a, _ := post(t, srv.URL, "", initialize)
	b, _ := post(t, srv.URL, "", initialize)
	sa, sb := a.Header.Get("Mcp-Session-Id"), b.Header.Get("Mcp-Session-Id")
	if sa == "" || sa == sb {
		t.Fatalf("sessions %q and %q", sa, sb)
	}
	_, ra := post(t, srv.URL, sa, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	_, rb := post(t, srv.URL, sb, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	pa := ra["result"].(map[string]any)["pid"]
	pb := rb["result"].(map[string]any)["pid"]
	if pa == pb || ra["result"].(map[string]any)["calls"] != float64(2) {
		t.Fatalf("two sessions share a server process: %v %v", ra, rb)
	}
	if resp, _ := post(t, srv.URL, "not-a-session", `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("an unknown session = %d", resp.StatusCode)
	}
}

func TestAServerRequestIsRefusedAndTheCallStillAnswered(t *testing.T) {
	srv := bridge(t, 4)
	init, _ := post(t, srv.URL, "", initialize)
	s := init.Header.Get("Mcp-Session-Id")
	resp, out := post(t, srv.URL, s, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"x"}}`)
	if resp.StatusCode != http.StatusOK || out["id"] != float64(7) || out["method"] != nil {
		t.Fatalf("call = %d %v", resp.StatusCode, out)
	}
}

func TestDeleteEndsTheSessionAndTheLimitHolds(t *testing.T) {
	srv := bridge(t, 1)
	init, _ := post(t, srv.URL, "", initialize)
	s := init.Header.Get("Mcp-Session-Id")
	if resp, _ := post(t, srv.URL, "", initialize); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("a session past the limit = %d", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodDelete, srv.URL, nil)
	req.Header.Set("Mcp-Session-Id", s)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %v %v", resp, err)
	}
	if resp, _ := post(t, srv.URL, s, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a deleted session = %d", resp.StatusCode)
	}
	if resp, _ := post(t, srv.URL, "", initialize); resp.StatusCode != http.StatusOK {
		t.Fatalf("a new session after delete = %d", resp.StatusCode)
	}
}

func probe(t *testing.T, url, session string) map[string]any {
	t.Helper()
	_, out := post(t, url, session, `{"jsonrpc":"2.0","id":5,"method":"probe"}`)
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("probe = %v", out)
	}
	return result
}

func TestEachSessionGetsItsOwnPrivateDirectoryRemovedWhenItEnds(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", root)
	t.Setenv("MCPSTDIO_LEAK", "another session's credential")
	b := &Bridge{Command: []string{exe}, Env: []string{"MCPSTDIO_CHILD=1", "HOME=/shared", "TMPDIR=/tmp"}, Sessions: 4}
	srv := httptest.NewServer(b)
	t.Cleanup(func() {
		srv.Close()
		b.ReapIdle(time.Now().Add(time.Hour))
	})
	a, _ := post(t, srv.URL, "", initialize)
	c, _ := post(t, srv.URL, "", initialize)
	pa := probe(t, srv.URL, a.Header.Get("Mcp-Session-Id"))
	pc := probe(t, srv.URL, c.Header.Get("Mcp-Session-Id"))
	home, _ := pa["home"].(string)
	if home == "" || home == pc["home"] || filepath.Dir(home) != root {
		t.Fatalf("sessions share a home or it is not under %s: %v %v", root, pa, pc)
	}
	if pa["cwd"] != home || pa["tmpdir"] != filepath.Join(home, "tmp") {
		t.Fatalf("a session works or keeps temporary files outside its own directory: %v", pa)
	}
	if pa["leak"] != "" {
		t.Fatalf("a session's process inherited the bridge's environment: %v", pa)
	}
	for _, dir := range []string{home, filepath.Join(home, "tmp")} {
		info, err := os.Stat(dir)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("%s: %v %v, want 0700", dir, info, err)
		}
	}
	req, _ := http.NewRequest(http.MethodDelete, srv.URL, nil)
	req.Header.Set("Mcp-Session-Id", a.Header.Get("Mcp-Session-Id"))
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %v %v", resp, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(home); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("an ended session's directory %s is left behind", home)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(pc["home"].(string)); err != nil {
		t.Fatalf("ending one session removed another's directory: %v", err)
	}
}

func TestAChildGetsOnlyTheEnvironmentItIsGiven(t *testing.T) {
	got := ChildEnv([]string{"PATH=/usr/bin", "GRAFANA_API_KEY=k", "OTHER_TOKEN=t", "HOME=/root", "LANG=C.UTF-8", "NOEQUALS"}, []string{"GRAFANA_API_KEY"})
	want := []string{"PATH=/usr/bin", "GRAFANA_API_KEY=k", "LANG=C.UTF-8"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("env = %v, want %v", got, want)
	}
}
