package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const contentTestToken = "content-test-token-0123456789abcdef"

func parsedPrincipal(t *testing.T, consumer string) OperatorPrincipal {
	t.Helper()
	consumers, err := ParseOperatorConsumers("["+consumer+"]", func(string) (string, bool) { return contentTestToken, true })
	if err != nil {
		t.Fatalf("%s: %v", consumer, err)
	}
	return consumers[0].Principal
}

func TestAConsumerThatNamesNoTeamReadsNoTeam(t *testing.T) {
	for _, tc := range []struct {
		consumer string
		all      bool
		silver   bool
	}{
		{`{"name":"a","tokenEnv":"T"}`, false, false},
		{`{"name":"a","tokenEnv":"T","teams":null}`, false, false},
		{`{"name":"a","tokenEnv":"T","teams":[]}`, false, false},
		{`{"name":"a","tokenEnv":"T","teams":["silver"]}`, false, true},
		{`{"name":"a","tokenEnv":"T","teams":["*"]}`, true, true},
	} {
		p := parsedPrincipal(t, tc.consumer)
		if p.AllowsTeam("bronze") != tc.all || p.AllowsTeam("silver") != tc.silver {
			t.Errorf("%s: bronze %v silver %v, want %v %v", tc.consumer, p.AllowsTeam("bronze"), p.AllowsTeam("silver"), tc.all, tc.silver)
		}
		if (p.Teams == nil) != tc.all {
			t.Errorf("%s: store scope %#v, want nil only for every Team", tc.consumer, p.Teams)
		}
	}
	for _, bad := range []string{`{"name":"a","tokenEnv":"T","teams":["*","silver"]}`, `{"name":"a","tokenEnv":"T","teams":["*","*"]}`} {
		if _, err := ParseOperatorConsumers("["+bad+"]", func(string) (string, bool) { return contentTestToken, true }); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestOnlyAConsumerGrantedContentReadsIt(t *testing.T) {
	for _, tc := range []struct {
		consumer string
		content  bool
	}{
		{`{"name":"a","tokenEnv":"T","teams":["*"]}`, false},
		{`{"name":"a","tokenEnv":"T","teams":["*"],"readContent":false}`, false},
		{`{"name":"a","tokenEnv":"T","teams":["*"],"readContent":true}`, true},
		{`{"name":"a","tokenEnv":"T","teams":["*"],"execute":true}`, true},
		{`{"name":"a","tokenEnv":"T","teams":["*"],"execute":true,"readContent":false}`, false},
	} {
		if got := parsedPrincipal(t, tc.consumer).CanReadContent; got != tc.content {
			t.Errorf("%s: readContent %v, want %v", tc.consumer, got, tc.content)
		}
	}
}

func contentServer(t *testing.T, consumers string) (*Server, int64, int64) {
	t.Helper()
	reset(t)
	shiftFixture(t, "content", 5, []store.Role{{Name: "reviewer", Cap: 1}})
	parsed, err := ParseOperatorConsumers(consumers, func(name string) (string, bool) {
		return strings.Repeat(strings.ToLower(name), 32), true
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: testStore, LeaseTTL: time.Minute, Log: slog.New(slog.DiscardHandler),
		WorkerSecurity: &WorkerSecurity{AllowLegacy: true}, OperatorConfig: OperatorConfig{Consumers: parsed}}
	code, claimed := postClaim(t, s.Handler(), `{"team":"bronze","role":"reviewer"}`)
	if code != http.StatusOK {
		t.Fatalf("claim: %d", code)
	}
	if w := workerRequest(s, "POST", "/api/v1/runs/"+claimed.RunToken+"/transcript", "", "pod", transcriptBody(1, 1, "SEED-TRANSCRIPT")); w.Code != http.StatusOK {
		t.Fatalf("append: %d %s", w.Code, w.Body)
	}
	var workItem int64
	if err := testPool.QueryRow(context.Background(), `SELECT work_item_id FROM agent_runs WHERE run_token = $1`, claimed.RunToken).Scan(&workItem); err != nil {
		t.Fatal(err)
	}
	return s, workItem, runIDOf(t, claimed.RunToken)
}

func TestAReadOnlyConsumerReadsMetadataButNoTranscriptContextOrNotes(t *testing.T) {
	s, workItem, run := contentServer(t, `[
		{"name":"metadata","tokenEnv":"META","teams":["*"]},
		{"name":"granted","tokenEnv":"GRANTED","teams":["*"],"readContent":true},
		{"name":"noteams","tokenEnv":"NOTEAMS","readContent":true}]`)
	metadata, granted, noTeams := strings.Repeat("meta", 32), strings.Repeat("granted", 32), strings.Repeat("noteams", 32)
	content := []string{
		fmt.Sprintf("/api/v1/operator/runs/%d/transcript", run),
		fmt.Sprintf("/api/v1/operator/runs/%d/briefing", run),
		fmt.Sprintf("/api/v1/operator/work-items/%d/context", workItem),
		"/api/v1/operator/knowledge/reviews",
	}
	for _, path := range content {
		w := operatorGet(s, metadata, path)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "content_forbidden") || strings.Contains(w.Body.String(), "SEED-TRANSCRIPT") {
			t.Errorf("metadata consumer read %s: %d %s", path, w.Code, w.Body)
		}
		if w := operatorGet(s, granted, path); w.Code != http.StatusOK {
			t.Errorf("granted consumer read %s: %d %s", path, w.Code, w.Body)
		}
	}
	if w := operatorGet(s, granted, content[0]); !strings.Contains(w.Body.String(), "SEED-TRANSCRIPT") {
		t.Errorf("granted consumer's transcript lacks the entry: %s", w.Body)
	}
	for _, path := range []string{fmt.Sprintf("/api/v1/operator/work-items/%d", workItem), fmt.Sprintf("/api/v1/operator/runs/%d", run)} {
		if w := operatorGet(s, metadata, path); w.Code != http.StatusOK {
			t.Errorf("metadata consumer could not read %s: %d %s", path, w.Code, w.Body)
		}
	}

	for _, path := range []string{fmt.Sprintf("/api/v1/operator/work-items/%d", workItem), content[0]} {
		if w := operatorGet(s, noTeams, path); w.Code != http.StatusNotFound {
			t.Errorf("a consumer naming no team read %s: %d %s", path, w.Code, w.Body)
		}
	}
	var list struct {
		Items []work.WorkItem `json:"items"`
	}
	w := operatorGet(s, noTeams, "/api/v1/operator/work-items")
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != http.StatusOK || len(list.Items) != 0 {
		t.Errorf("a consumer naming no team lists %d %s", w.Code, w.Body)
	}
}
