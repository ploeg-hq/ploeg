package clickup

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

const (
	connToken   = "fixture-connection-token"
	staticToken = "fixture-static-token"
)

type fakeConnections struct {
	conn  Connection
	bound map[string]bool
	held  map[string]string
}

func (f fakeConnections) HandOverAgents(_ context.Context, userID string) []Connection {
	if userID == f.conn.Agent {
		return []Connection{f.conn}
	}
	return nil
}

func (f fakeConnections) BoundThrough(_ context.Context, listID string) (Connection, bool) {
	return f.conn, f.bound[listID]
}

func (f fakeConnections) TaskList(_ context.Context, taskID string) (string, bool) {
	list, ok := f.held[taskID]
	return list, ok
}

// connectionAPI answers only connToken, serving tasks in the Lists given.
func connectionAPI(t *testing.T, lists map[string]string) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var writes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != connToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/task/")
		if r.Method == http.MethodPost {
			mu.Lock()
			writes = append(writes, r.URL.Path)
			mu.Unlock()
			return
		}
		list, ok := lists[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "status": map[string]string{"status": "open", "type": "open"}, "list": map[string]string{"id": list}})
	}))
	t.Cleanup(srv.Close)
	return srv, &writes
}

func assigneeDelivery(t *testing.T, task, field, side, member string) *http.Request {
	t.Helper()
	body := fmt.Sprintf(`{"event":"taskAssigneeUpdated","task_id":%q,"history_items":[{"id":"h1","field":%q,%q:%s}]}`, task, field, side, member)
	r := httptest.NewRequest(http.MethodPost, WebhookPath, strings.NewReader(body))
	mac := hmac.New(sha256.New, []byte("s"))
	mac.Write([]byte(body))
	r.Header.Set("X-Signature", hex.EncodeToString(mac.Sum(nil)))
	return r
}

func TestAnAssignmentToAConnectionsOwnAccountHandsOverOnlyOnAListBoundThroughIt(t *testing.T) {
	api, writes := connectionAPI(t, map[string]string{"t-bound": "901", "t-other": "555"})
	logs := &bytes.Buffer{}
	conns := fakeConnections{conn: Connection{ID: "cu-bot", Agent: "4242", Token: connToken, BaseURL: api.URL}, bound: map[string]bool{"901": true}, held: map[string]string{}}
	p := &Provider{Secret: "s", DefaultTeam: "default", Agents: []string{"static-agent"}, Token: staticToken, BaseURL: "http://127.0.0.1:1",
		Connections: conns, Log: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}

	events, err := p.ParseWebhook(assigneeDelivery(t, "t-bound", "assignee_add", "after", `{"id":4242,"username":"Ploeg Bot"}`))
	if err != nil || len(events) != 1 || events[0].Kind != provider.TrackerAssigned {
		t.Fatalf("agent assignment on a bound List = %+v, %v; want one assignment", events, err)
	}
	item, err := p.FetchItem(context.Background(), "t-bound")
	if err != nil || item.ExternalScope != "901" {
		t.Fatalf("first mirror = %+v, %v; want it read through the connection before the Work Item exists", item, err)
	}
	if err := p.Comment(context.Background(), "t-bound", "hello"); err != nil || len(*writes) != 1 {
		t.Fatalf("comment = %v, writes %v; want it written with the connection's token", err, *writes)
	}

	events, err = p.ParseWebhook(assigneeDelivery(t, "t-bound", "assignee_rem", "before", `{"id":4242,"username":"Ploeg Bot"}`))
	if err != nil || len(events) != 1 || events[0].Kind != provider.TrackerUnassigned {
		t.Fatalf("agent removal = %+v, %v; want one unassignment", events, err)
	}

	for name, req := range map[string]*http.Request{
		"someone else on a bound List":       assigneeDelivery(t, "t-bound", "assignee_add", "after", `{"id":7,"username":"a-person"}`),
		"the agent on a List not bound here": assigneeDelivery(t, "t-other", "assignee_add", "after", `{"id":4242,"username":"Ploeg Bot"}`),
	} {
		if events, err := p.ParseWebhook(req); err != nil || len(events) != 0 {
			t.Errorf("%s = %+v, %v; want nothing", name, events, err)
		}
	}

	events, err = p.ParseWebhook(assigneeDelivery(t, "t-static", "assignee_add", "after", `{"id":8,"username":"static-agent"}`))
	if err != nil || len(events) != 1 || events[0].Kind != provider.TrackerAssigned {
		t.Fatalf("static agent = %+v, %v; static agents must keep working", events, err)
	}
	if strings.Contains(logs.String(), connToken) {
		t.Fatal("the connection's token reached a log line")
	}
}

func TestAHeldTaskOnAStaticListKeepsTheStaticCredential(t *testing.T) {
	var auth []string
	static := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, `{}`)
	}))
	defer static.Close()
	api, writes := connectionAPI(t, nil)
	conns := fakeConnections{conn: Connection{ID: "cu-bot", Agent: "4242", Token: connToken, BaseURL: api.URL},
		bound: map[string]bool{"901": true}, held: map[string]string{"t-static": "10", "t-bound": "901"}}
	p := &Provider{Token: staticToken, BaseURL: static.URL, Connections: conns}
	if err := p.Comment(context.Background(), "t-static", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := p.Comment(context.Background(), "t-bound", "hello"); err != nil {
		t.Fatal(err)
	}
	if len(auth) != 1 || auth[0] != staticToken || len(*writes) != 1 {
		t.Fatalf("static calls %v, connection writes %v; want each task written with its own List's credential", auth, *writes)
	}
}

func TestAConnectionWithoutAWorkspaceRegistersAListWebhookInTheListsWorkspace(t *testing.T) {
	var created map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != connToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/list/901" && r.Method == http.MethodGet:
			fmt.Fprint(w, `{"id":"901","space":{"id":"70"},"folder":{"id":"80"}}`)
		case r.URL.Path == "/list/901/member":
			fmt.Fprint(w, `{"members":[]}`)
		case r.URL.Path == "/team" && r.Method == http.MethodGet:
			fmt.Fprint(w, `{"teams":[{"id":"1"},{"id":"2"}]}`)
		case r.URL.Path == "/team/1/space":
			fmt.Fprint(w, `{"spaces":[{"id":"60"}]}`)
		case r.URL.Path == "/team/2/space":
			fmt.Fprint(w, `{"spaces":[{"id":"70"}]}`)
		case r.URL.Path == "/team/2/webhook" && r.Method == http.MethodGet:
			if created == nil {
				fmt.Fprint(w, `{"webhooks":[]}`)
				return
			}
			fmt.Fprint(w, `{"webhooks":[{"id":"w1","endpoint":"https://ploeg.example/webhooks/tracker/clickup","events":["taskAssigneeUpdated","taskStatusUpdated","taskUpdated"],"list_id":901}]}`)
		case r.URL.Path == "/team/2/webhook" && r.Method == http.MethodPost:
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &created)
			fmt.Fprint(w, `{"id":"w1","webhook":{"id":"w1","secret":"fixture-signing-value"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := &Provider{BaseURL: srv.URL, Token: connToken}
	endpoint := "https://ploeg.example/webhooks/tracker/clickup"
	if got := p.CheckWebhooks(context.Background(), []string{"901"}, endpoint); got[0].Webhook != provider.WebhookMissing {
		t.Fatalf("before registration = %+v; want missing, not unreadable", got)
	}
	sink := &recordingSink{}
	if id, err := p.RegisterScopeWebhook(context.Background(), "901", endpoint, sink); err != nil || id != "w1" || sink.id != "w1" {
		t.Fatalf("register = %q, %v, sink kept %q", id, err, sink.id)
	}
	events, _ := created["events"].([]any)
	if created["list_id"] != float64(901) || len(events) == 0 || events[0] != "taskAssigneeUpdated" {
		t.Fatalf("create body = %v; want a List webhook delivering assignments", created)
	}
	if got := p.CheckWebhooks(context.Background(), []string{"901"}, endpoint); got[0].Webhook != provider.WebhookPresent {
		t.Fatalf("after registration = %+v; want present", got)
	}
}

func TestConnectionBaseURLAddsTheAPIVersionTheProbeUses(t *testing.T) {
	if got := ConnectionBaseURL(provider.ConnectionInstance{APIURL: "https://clickup.example/api/"}); got != "https://clickup.example/api/v2" {
		t.Fatalf("base = %q", got)
	}
	if got := ConnectionBaseURL(provider.ConnectionInstance{}); got != DefaultBaseURL {
		t.Fatalf("default base = %q, want %q", got, DefaultBaseURL)
	}
}

func TestAReceiverWithoutAStaticSecretNeverVerifiesWithAnEmptyKey(t *testing.T) {
	body := `{"event":"taskAssigneeUpdated","webhook_id":"wh-1","task_id":"t1","history_items":[{"id":"h1","field":"assignee_add","after":{"id":1}}]}`
	mac := hmac.New(sha256.New, []byte(""))
	mac.Write([]byte(body))
	r := httptest.NewRequest(http.MethodPost, WebhookPath, strings.NewReader(body))
	r.Header.Set("X-Signature", hex.EncodeToString(mac.Sum(nil)))
	p := &Provider{WebhookSecret: func(context.Context, string) (string, bool) { return "", false }}
	if _, err := p.ParseWebhook(r); err == nil {
		t.Fatal("a delivery signed with an empty key was accepted by a receiver without a static secret")
	}
}
