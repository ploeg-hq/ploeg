package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/clickup"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/target"
	"github.com/ploeg-hq/ploeg/pkg/trackerbinding"
)

const (
	handOverToken   = "cu-connection-SECRET-5e1d"
	handOverAccount = "4242"
	handOverConn    = "clickup-bot"
)

// connectionClickUp is the ClickUp a store-backed connection reaches: it
// answers only the connection's own token and records every write.
type connectionClickUp struct {
	mu       sync.Mutex
	lists    map[string]string
	comments []string
	auth     []string
	srv      *httptest.Server
}

func newConnectionClickUp(t *testing.T) *connectionClickUp {
	t.Helper()
	f := &connectionClickUp{lists: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != handOverToken {
			http.Error(w, `{"err":"Token invalid"}`, http.StatusUnauthorized)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/task/")
		if r.Method == http.MethodPost && strings.HasSuffix(id, "/comment") {
			f.comments = append(f.comments, strings.TrimSuffix(id, "/comment"))
			return
		}
		list, ok := f.lists[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "name": "handed fixture", "date_updated": "1",
			"status": map[string]string{"status": "open", "type": "open"}, "list": map[string]string{"id": list}})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *connectionClickUp) set(task, list string) {
	f.mu.Lock()
	f.lists[task] = list
	f.mu.Unlock()
}

// handOverConnections is what ploegd's clickupConnections answers, over a
// real binding registry and the Work Items in the store.
type handOverConnections struct {
	reg  *trackerbinding.Registry
	conn clickup.Connection
}

func (h handOverConnections) HandOverAgents(_ context.Context, userID string) []clickup.Connection {
	if userID == h.conn.Agent {
		return []clickup.Connection{h.conn}
	}
	return nil
}

func (h handOverConnections) BoundThrough(_ context.Context, listID string) (clickup.Connection, bool) {
	if id, ok := h.reg.RoutedConnection(listID); ok && id == h.conn.ID {
		return h.conn, true
	}
	return clickup.Connection{}, false
}

func (h handOverConnections) TaskList(ctx context.Context, taskID string) (string, bool) {
	_, item, err := testStore.TrackerWorkItemID(ctx, "clickup", taskID)
	return item.ExternalScope, err == nil && item.ExternalScope != ""
}

func clickupMemberHook(t *testing.T, h http.Handler, taskID, field, userID, username string) {
	t.Helper()
	side := "after"
	if field == "assignee_rem" {
		side = "before"
	}
	body := fmt.Sprintf(`{"event":"taskAssigneeUpdated","task_id":%q,"history_items":[{"id":"%s-%s-%s","field":%q,"date":"1759310400000",%q:{"id":%s,"username":%q}}]}`,
		taskID, taskID, field, userID, field, side, userID, username)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, signedTrackerHook("clickup", body))
	if w.Code != http.StatusAccepted {
		t.Fatalf("%s %s on %s: HTTP %d", field, username, taskID, w.Code)
	}
}

func TestAnAssignmentToTheConnectionsOwnAccountAdmitsTheTaskToTheBoundTeam(t *testing.T) {
	resetBindings(t)
	ctx := context.Background()
	logs := &bytes.Buffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cfg := &config.File{
		Targets: map[string]config.Target{"app": {Repo: "acme/app"}, "docs": {Repo: "acme/docs"}},
		Teams:   map[string]config.Team{"silver": {}, "gold": {}},
		Trackers: config.Trackers{Clickup: config.TrackerConfig{Agents: []string{"Agent-Member"},
			Projects: []config.Project{{ID: "10", Team: "gold", Default: "app"}}}},
	}
	table, err := cfg.RoutingTable(ctx, nil, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := target.New(table, "forgejo")
	if err != nil {
		t.Fatal(err)
	}
	remote := newConnectionClickUp(t)
	runtime := trackerbinding.Connection{ID: handOverConn, Kind: "clickup", Runtime: true, State: trackerbinding.ConnectionActive, HandOverAgent: true}
	reg := &trackerbinding.Registry{Store: testStore, Config: cfg, Static: table, DefaultForge: "forgejo",
		Connections: trackerbinding.StaticConnections{handOverConn: runtime}, Live: target.NewLive(resolver), Log: log}
	for _, scope := range []string{"901", "10"} {
		if _, _, err := testStore.CreateTrackerBinding(ctx, store.NewTrackerBinding{Team: "silver", Connection: handOverConn, ScopeID: scope,
			TrackerBindingFields: store.TrackerBindingFields{Default: "docs"}, State: store.BindingActive,
			Webhook: store.BindingWebhook{State: provider.WebhookPresent, Source: store.BindingWebhookExisting}, Max: 10}, store.BindingAudit{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := reg.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if _, routed := reg.RoutedConnection("10"); routed {
		t.Fatal("a binding of a statically routed List routes; static routing must win")
	}

	board := newClickupBoard(t)
	board.set("cu-static", clickupTask{statusType: "open"})
	cu := board.provider()
	cu.Agents, cu.Log = []string{"Agent-Member"}, log
	cu.Connections = handOverConnections{reg: reg, conn: clickup.Connection{ID: handOverConn, Agent: handOverAccount, Token: handOverToken, BaseURL: remote.srv.URL}}
	s := &Server{Store: testStore, Log: log, Targets: reg.Live, TrackerBindings: &TrackerBindings{Registry: reg},
		Trackers: map[string]provider.TrackerProvider{"clickup": cu}, ScopeTeams: cfg.ScopeTeams()}
	h := s.Handler()

	remote.set("cu-handed", "901")
	clickupMemberHook(t, h, "cu-handed", "assignee_add", handOverAccount, "Ploeg Bot")
	state, team, found := clickupItem(t, "cu-handed")
	if !found || state != "queued" || team != "silver" {
		t.Fatalf("assigning the connection's account = %q/%q (found %v); want queued for the bound Team", state, team, found)
	}
	if n, route, _ := clickupRoute(t, "cu-handed"); n != 1 || route.Repo != "docs" {
		t.Fatalf("handed task routed to %+v (%d rows); want the binding's default target", route, n)
	}

	remote.set("cu-person", "901")
	clickupMemberHook(t, h, "cu-person", "assignee_add", "7", "a-person")
	if _, _, found := clickupItem(t, "cu-person"); found {
		t.Fatal("assigning someone else on a bound List queued work")
	}

	remote.set("cu-on-static", "10")
	clickupMemberHook(t, h, "cu-on-static", "assignee_add", handOverAccount, "Ploeg Bot")
	if _, _, found := clickupItem(t, "cu-on-static"); found {
		t.Fatal("the connection's account handed over work on a statically routed List")
	}

	clickupMemberHook(t, h, "cu-static", "assignee_add", "8", "agent-member")
	if state, team, _ := clickupItem(t, "cu-static"); state != "queued" || team != "gold" {
		t.Fatalf("static agent assignment = %q/%q; want queued for the static project's Team", state, team)
	}

	if err := cu.Comment(ctx, "cu-handed", "Pull request opened"); err != nil {
		t.Fatal(err)
	}
	if err := cu.Comment(ctx, "cu-static", "Pull request opened"); err != nil {
		t.Fatal(err)
	}
	remote.mu.Lock()
	comments := append([]string(nil), remote.comments...)
	for _, a := range remote.auth {
		if a != handOverToken {
			t.Errorf("the connection's ClickUp was called with %q; want only the connection's token", a)
		}
	}
	remote.mu.Unlock()
	if len(comments) != 1 || comments[0] != "cu-handed" {
		t.Fatalf("comments through the connection = %v; want the handed task's only", comments)
	}
	board.mu.Lock()
	staticComments := board.comments
	board.mu.Unlock()
	if staticComments["cu-handed"] != 0 || staticComments["cu-static"] != 1 {
		t.Fatalf("static comments = %v; the handed task must not be written with the static token", staticComments)
	}

	clickupMemberHook(t, h, "cu-handed", "assignee_rem", handOverAccount, "Ploeg Bot")
	if state, _, _ := clickupItem(t, "cu-handed"); state == "queued" {
		t.Fatal("removing the connection's account left the work queued")
	}

	var rows []string
	for _, q := range []string{`SELECT row_to_json(w)::text FROM work_items w`, `SELECT row_to_json(a)::text FROM audit_log a`, `SELECT row_to_json(b)::text FROM tracker_bindings b`} {
		res, err := testPool.Query(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		for res.Next() {
			var row string
			_ = res.Scan(&row)
			rows = append(rows, row)
		}
		res.Close()
	}
	if strings.Contains(logs.String(), handOverToken) || strings.Contains(strings.Join(rows, "\n"), handOverToken) {
		t.Fatal("the connection's token reached a log line or a database row")
	}
}

func TestTheConnectionsOwnAccountBecomesAHandOverAgentOnlyWhereTheAdapterRecognisesIt(t *testing.T) {
	rig := newConnRig(t)
	start := "/api/v1/operator/connection-flows"
	body := map[string]any{"port": "tracker", "kind": "clickup", "instance": "clickup-cloud", "method": "oauth", "redirectUri": connRedirect, "handOverAgent": true}
	if code, out := rig.call("POST", start, rig.me(), body); code != 422 || errCode(out) != "capability_missing" {
		t.Fatalf("handOverAgent on a kind that does not declare it = %d %v; want 422 capability_missing", code, out)
	}

	cu := rig.svc.Connectors["clickup"]
	cu.HandOverAgent = true
	rig.svc.Connectors["clickup"] = cu
	state, _ := rig.startClickUp(map[string]any{"id": "team-bot", "handOverAgent": true})
	code, out := rig.call("POST", start+"/complete", rig.me(), map[string]any{"state": state, "code": "c1"})
	if code != 201 {
		t.Fatalf("complete = %d %v", code, out)
	}
	conn := out["connection"].(map[string]any)
	if conn["handOverAgent"] != true || conn["account"].(map[string]any)["id"] != "42" {
		t.Fatalf("connection = %v; want handOverAgent true and the probed account id", conn)
	}
	row, err := testStore.Connection(context.Background(), "default", "team-bot")
	if err != nil {
		t.Fatal(err)
	}
	if !row.HandOverAgent || row.AccountID != "42" {
		t.Fatalf("row hand_over_agent %v account_id %q; want the probe's ClickUp user id as the hand-over agent", row.HandOverAgent, row.AccountID)
	}

	revision := fmt.Sprintf(`"%d"`, row.Revision)
	if code, out := rig.call("PATCH", "/api/v1/operator/tracker-connections/team-bot", rig.me(), map[string]any{"handOverAgent": false}, "If-Match", revision); code != 200 || out["connection"].(map[string]any)["handOverAgent"] != false {
		t.Fatalf("patch off = %d %v", code, out)
	}
	cu.HandOverAgent = false
	rig.svc.Connectors["clickup"] = cu
	row, _ = testStore.Connection(context.Background(), "default", "team-bot")
	if code, out := rig.call("PATCH", "/api/v1/operator/tracker-connections/team-bot", rig.me(), map[string]any{"handOverAgent": true}, "If-Match", fmt.Sprintf(`"%d"`, row.Revision)); code != 422 || errCode(out) != "capability_missing" {
		t.Fatalf("patch on without the capability = %d %v; want 422 capability_missing", code, out)
	}
	rig.assertNoSecrets(clickUpToken, clickUpSecret)
}

const runtimeHookSecret = "fixture-runtime-webhook-signing"

func runtimeHook(taskID, webhookID, secret, userID string) *http.Request {
	body := fmt.Sprintf(`{"event":"taskAssigneeUpdated","webhook_id":%q,"task_id":%q,"history_items":[{"id":"%s-%s","field":"assignee_add","date":"1759310400000","after":{"id":%s,"username":"Ploeg Bot"}}]}`,
		webhookID, taskID, taskID, webhookID, userID)
	r := httptest.NewRequest(http.MethodPost, "/webhooks/tracker/clickup", strings.NewReader(body))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	r.Header.Set("X-Signature", hex.EncodeToString(mac.Sum(nil)))
	return r
}

func TestARuntimeBindingsWebhookDeliversWithOrWithoutAStaticClickUp(t *testing.T) {
	for _, withStatic := range []bool{false, true} {
		t.Run(fmt.Sprintf("static=%v", withStatic), func(t *testing.T) {
			resetBindings(t)
			ctx := context.Background()
			if _, err := testPool.Exec(ctx, `DELETE FROM forge_deliveries`); err != nil {
				t.Fatal(err)
			}
			logs := &bytes.Buffer{}
			log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			cfg := &config.File{Targets: map[string]config.Target{"app": {Repo: "acme/app"}, "docs": {Repo: "acme/docs"}},
				Teams: map[string]config.Team{"silver": {}, "gold": {}}}
			if withStatic {
				cfg.Trackers.Clickup = config.TrackerConfig{Agents: []string{"Agent-Member"}, Projects: []config.Project{{ID: "10", Team: "gold", Default: "app"}}}
			}
			table, err := cfg.RoutingTable(ctx, nil, slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatal(err)
			}
			resolver, err := target.New(table, "forgejo")
			if err != nil {
				t.Fatal(err)
			}
			runtime := trackerbinding.Connection{ID: handOverConn, Kind: "clickup", Runtime: true, State: trackerbinding.ConnectionActive, HandOverAgent: true}
			reg := &trackerbinding.Registry{Store: testStore, Config: cfg, Static: table, DefaultForge: "forgejo",
				Connections: trackerbinding.StaticConnections{handOverConn: runtime}, Live: target.NewLive(resolver), Log: log}
			if _, _, err := testStore.CreateTrackerBinding(ctx, store.NewTrackerBinding{Team: "silver", Connection: handOverConn, ScopeID: "901",
				TrackerBindingFields: store.TrackerBindingFields{Default: "docs"}, State: store.BindingActive,
				Webhook: store.BindingWebhook{State: provider.WebhookPresent, ID: "wh-live", Source: store.BindingWebhookRegistered}, Max: 10}, store.BindingAudit{}); err != nil {
				t.Fatal(err)
			}
			if err := reg.Rebuild(ctx); err != nil {
				t.Fatal(err)
			}
			kept := map[string]string{"wh-live": runtimeHookSecret, "wh-removed": "fixture-removed-webhook-signing"}
			remote := newConnectionClickUp(t)
			remote.set("cu-runtime", "901")
			remote.set("cu-forged", "901")
			var cu *clickup.Provider
			if withStatic {
				board := newClickupBoard(t)
				board.set("cu-static", clickupTask{statusType: "open"})
				cu = board.provider()
				cu.Agents = []string{"Agent-Member"}
			} else {
				cu = &clickup.Provider{DefaultTeam: "bronze", ConnectionsOnly: true}
			}
			cu.Log = log
			cu.Connections = handOverConnections{reg: reg, conn: clickup.Connection{ID: handOverConn, Agent: handOverAccount, Token: handOverToken, BaseURL: remote.srv.URL}}
			cu.WebhookSecret = func(_ context.Context, id string) (string, bool) {
				if !reg.RegisteredWebhook(id) {
					return "", false
				}
				secret, ok := kept[id]
				return secret, ok
			}
			s := &Server{Store: testStore, Log: log, Targets: reg.Live, TrackerBindings: &TrackerBindings{Registry: reg},
				Trackers: map[string]provider.TrackerProvider{"clickup": cu}, ScopeTeams: cfg.ScopeTeams()}
			h := s.Handler()
			var bodies []string
			send := func(r *http.Request) int {
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				bodies = append(bodies, w.Body.String())
				return w.Code
			}

			if code := send(runtimeHook("cu-runtime", "wh-live", runtimeHookSecret, handOverAccount)); code != http.StatusAccepted {
				t.Fatalf("a delivery signed with its binding webhook's secret = HTTP %d", code)
			}
			if state, team, found := clickupItem(t, "cu-runtime"); !found || state != "queued" || team != "silver" {
				t.Fatalf("runtime hand-over = %q/%q (found %v); want queued for the bound Team", state, team, found)
			}
			bad := send(runtimeHook("cu-forged", "wh-live", "not-the-secret", handOverAccount))
			unknown := send(runtimeHook("cu-forged", "wh-unknown", runtimeHookSecret, handOverAccount))
			removed := send(runtimeHook("cu-forged", "wh-removed", kept["wh-removed"], handOverAccount))
			if bad != http.StatusBadRequest || unknown != bad || removed != bad {
				t.Fatalf("bad signature %d, unknown webhook %d, webhook of no live binding %d; want the same refusal for each", bad, unknown, removed)
			}
			if bodies[1] != bodies[2] || bodies[2] != bodies[3] {
				t.Fatalf("refusals differ (%q, %q, %q); they must not say which webhook ids exist", bodies[1], bodies[2], bodies[3])
			}
			if _, _, found := clickupItem(t, "cu-forged"); found {
				t.Fatal("a refused delivery queued work")
			}
			if withStatic {
				clickupMemberHook(t, h, "cu-static", "assignee_add", "8", "agent-member")
				if state, team, _ := clickupItem(t, "cu-static"); state != "queued" || team != "gold" {
					t.Fatalf("static agent assignment = %q/%q; the static path must keep working", state, team)
				}
			}

			var rows []string
			for _, q := range []string{`SELECT row_to_json(w)::text FROM work_items w`, `SELECT row_to_json(a)::text FROM audit_log a`, `SELECT row_to_json(b)::text FROM tracker_bindings b`, `SELECT row_to_json(d)::text FROM forge_deliveries d`} {
				res, err := testPool.Query(ctx, q)
				if err != nil {
					t.Fatal(err)
				}
				for res.Next() {
					var row string
					_ = res.Scan(&row)
					rows = append(rows, row)
				}
				res.Close()
			}
			seen := logs.String() + strings.Join(rows, "\n") + strings.Join(bodies, "\n")
			for _, secret := range []string{handOverToken, runtimeHookSecret, kept["wh-removed"]} {
				if strings.Contains(seen, secret) {
					t.Fatalf("a secret reached a log line, row or response")
				}
			}
		})
	}
}
