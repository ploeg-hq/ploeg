package clickup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestFetchItemKeepsTheMarkdownDescriptionAndTheTaskPage(t *testing.T) {
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		fmt.Fprint(w, `{"id":"t1","name":"Fix","url":"https://app.clickup.com/t/9012/ENG-42","custom_id":"ENG-42",
			"description":"Steps do it","text_content":"Steps do it","markdown_description":"## Steps\n\n- do **it**",
			"status":{"type":"open"},"list":{"id":"901"}}`)
	}))
	defer srv.Close()
	item, err := (&Provider{BaseURL: srv.URL, Token: "pk"}).FetchItem(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(query, "include_markdown_description=true") {
		t.Errorf("the read did not ask for the markdown description: %q", query)
	}
	if item.Description != "## Steps\n\n- do **it**" {
		t.Errorf("description = %q, want the markdown body", item.Description)
	}
	if item.URL != "https://app.clickup.com/t/9012/ENG-42" {
		t.Errorf("url = %q, want the page ClickUp names", item.URL)
	}
}

func TestFetchItemFallsBackToTheAppLinkWhenClickUpNamesNoPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"id":"86abc","name":"Fix","status":{"type":"open"},"list":{"id":"901"}}`)
	}))
	defer srv.Close()
	item, err := (&Provider{BaseURL: srv.URL, Token: "pk"}).FetchItem(context.Background(), "86abc")
	if err != nil || item.URL != "https://app.clickup.com/t/86abc" {
		t.Fatalf("url = %q, err %v", item.URL, err)
	}
	var _ provider.TaskLinker = (*Provider)(nil)
	if got := (&Provider{}).TaskURL(""); got != "" {
		t.Errorf("an empty id links to %q", got)
	}
}

func TestTheWebhookSnapshotLinksTheTask(t *testing.T) {
	events, err := post(t, &Provider{DefaultTeam: "default"}, map[string]any{"event": "taskUpdated", "task_id": "86abc"})
	if err != nil || len(events) != 1 || events[0].Item.URL != "https://app.clickup.com/t/86abc" {
		t.Fatalf("events = %+v, err %v", events, err)
	}
}

func TestCommentSendsRichTextSoAPersonReadsNoMarkup(t *testing.T) {
	var body struct {
		CommentText string      `json:"comment_text"`
		Comment     []richBlock `json:"comment"`
		NotifyAll   bool        `json:"notify_all"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &body); err != nil {
			t.Errorf("body %s: %v", b, err)
		}
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	md := "Ploeg finished this item. Its pull request is ready for review.\n\n**Outcome:** done\n**Pull request:** https://forge.example/o/r/pulls/7.\n\n<sub>Posted by Ploeg.</sub>"
	if err := (&Provider{BaseURL: srv.URL, Token: "pk"}).Comment(context.Background(), "t1", md); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body.CommentText, "**") || strings.Contains(body.CommentText, "<sub>") {
		t.Errorf("comment_text still carries markup: %q", body.CommentText)
	}
	if !strings.Contains(body.CommentText, "Outcome: done") {
		t.Errorf("comment_text lost words: %q", body.CommentText)
	}
	want := map[string]map[string]any{
		"Outcome:":                          {"bold": true},
		"https://forge.example/o/r/pulls/7": {"link": "https://forge.example/o/r/pulls/7"},
		"Posted by Ploeg.":                  {"italic": true},
	}
	for _, b := range body.Comment {
		if attrs, ok := want[b.Text]; ok {
			for k, v := range attrs {
				if b.Attributes[k] != v {
					t.Errorf("block %q attribute %s = %v, want %v", b.Text, k, b.Attributes[k], v)
				}
			}
			delete(want, b.Text)
		}
	}
	if len(want) != 0 {
		t.Errorf("blocks missing: %v in %+v", want, body.Comment)
	}
}

func TestRichCommentFormatsListsCodeAndLinks(t *testing.T) {
	plain, blocks := richComment("- first `x`\n1. [docs](https://d.example)\n```\ngo test\n```")
	if plain != "first x\ndocs\ngo test" {
		t.Errorf("plain = %q", plain)
	}
	var lists, code []string
	for _, b := range blocks {
		if l, ok := b.Attributes["list"].(map[string]any); ok {
			lists = append(lists, l["list"].(string))
		}
		if _, ok := b.Attributes["code-block"]; ok {
			code = append(code, b.Text)
		}
		if b.Text == "docs" && b.Attributes["link"] != "https://d.example" {
			t.Errorf("link block = %+v", b)
		}
		if b.Text == "x" && b.Attributes["code"] != true {
			t.Errorf("code block = %+v", b)
		}
	}
	if strings.Join(lists, ",") != "bullet,ordered" || len(code) != 1 {
		t.Errorf("lists %v, code lines %v in %+v", lists, code, blocks)
	}
}

func TestSetStatusMovesEachMappedStateToItsListStatus(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Status string `json:"status"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		got = append(got, r.Method+" "+r.URL.Path+" "+b.Status)
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	p := &Provider{BaseURL: srv.URL, Token: "pk", DoneStatus: "complete", Statuses: map[work.State]string{
		work.StateActive: "in progress", work.StateAwaitingReview: "in review", work.StateNeedsHuman: "blocked"}}
	for _, st := range []work.State{work.StateActive, work.StateAwaitingReview, work.StateNeedsHuman, work.StateStale, work.StateDone} {
		if err := p.SetStatus(context.Background(), "t1", st); err != nil {
			t.Fatalf("%s: %v", st, err)
		}
	}
	want := "PUT /task/t1 in progress|PUT /task/t1 in review|PUT /task/t1 blocked|PUT /task/t1 complete"
	if strings.Join(got, "|") != want {
		t.Fatalf("writes = %q, want %q", strings.Join(got, "|"), want)
	}
	p.Statuses[work.StateDone] = "shipped"
	got = nil
	_ = p.SetStatus(context.Background(), "t1", work.StateDone)
	if len(got) != 1 || got[0] != "PUT /task/t1 shipped" {
		t.Fatalf("a mapped done did not win over DoneStatus: %v", got)
	}
}

func TestDependenciesReportTheTasksThisOneWaitsOn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/task/t1":
			fmt.Fprint(w, `{"id":"t1","dependencies":[
				{"task_id":"t1","depends_on":"t2","type":1},
				{"task_id":"t1","depends_on":"t3","type":1},
				{"task_id":"t9","depends_on":"t1","type":0},
				{"task_id":"t1","depends_on":"t2","type":1}]}`)
		case "/task/t2":
			fmt.Fprint(w, `{"id":"t2","name":"Schema","status":{"type":"closed"}}`)
		case "/task/t3":
			fmt.Fprint(w, `{"id":"t3","name":"API","status":{"type":"custom"}}`)
		default:
			t.Errorf("unexpected read of %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	var reader provider.DependencyReader = &Provider{BaseURL: srv.URL, Token: "pk"}
	deps, err := reader.Dependencies(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	want := []provider.TrackerDependency{
		{ExternalID: "t2", Title: "Schema", Kind: provider.DependencyBlockedBy, Done: true},
		{ExternalID: "t3", Title: "API", Kind: provider.DependencyBlockedBy, Done: false},
	}
	if fmt.Sprint(deps) != fmt.Sprint(want) {
		t.Fatalf("deps = %+v, want %+v", deps, want)
	}
}

func TestDependenciesNeedCredentials(t *testing.T) {
	if _, err := (&Provider{}).Dependencies(context.Background(), "t1"); err == nil {
		t.Fatal("an unconfigured provider reported dependencies")
	}
}
