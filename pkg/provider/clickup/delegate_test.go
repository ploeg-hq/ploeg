package clickup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const teamOptions = `[{"id":"opt-bronze","name":"Bronze","orderindex":0,"color":null},{"id":"opt-silver","name":"Silver","orderindex":1,"color":"#ccc"},{"id":"opt-gold","name":"Gold","orderindex":2}]`

func taskWithFields(fields string) string {
	return fmt.Sprintf(`{"id":"t1","name":"Fix the thing","date_updated":"1756100000000","list":{"id":"901"},"custom_fields":[%s]}`, fields)
}

type delegateServer struct {
	srv      *httptest.Server
	reads    atomic.Int32
	comments []map[string]any
}

func newDelegateServer(t *testing.T, taskBody string) *delegateServer {
	t.Helper()
	d := &delegateServer{}
	d.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comment") {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			d.comments = append(d.comments, body)
			fmt.Fprint(w, `{"id":"c1"}`)
			return
		}
		d.reads.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, taskBody)
	}))
	t.Cleanup(d.srv.Close)
	return d
}

func (d *delegateServer) provider() *Provider {
	return &Provider{BaseURL: d.srv.URL, Token: "pk", HC: d.srv.Client()}
}

func TestDelegateReadsTheTeamFieldInBothValueShapes(t *testing.T) {
	cases := map[string]string{
		"option index":         `{"id":"f1","name":"Team","type":"drop_down","value":1,"type_config":{"options":` + teamOptions + `}}`,
		"option id":            `{"id":"f1","name":"Team","type":"drop_down","value":"opt-silver","type_config":{"options":` + teamOptions + `}}`,
		"index as a string":    `{"id":"f1","name":"Team","type":"drop_down","value":"1","type_config":{"options":` + teamOptions + `}}`,
		"name in another case": `{"id":"f1","name":"team","type":"drop_down","value":1,"type_config":{"options":` + teamOptions + `}}`,
	}
	for name, field := range cases {
		t.Run(name, func(t *testing.T) {
			d := newDelegateServer(t, taskWithFields(field))
			team, source, err := d.provider().Delegate(context.Background(), work.WorkItem{ExternalID: "t1"})
			if err != nil {
				t.Fatal(err)
			}
			if team != "silver" || source != provider.DelegateSourceField {
				t.Fatalf("Delegate = %q from %q, want silver from field", team, source)
			}
		})
	}
}

func TestDelegateNamesNoTeamWhenTheFieldIsAbsentUnsetOrUnknown(t *testing.T) {
	cases := map[string]string{
		"no custom fields":   ``,
		"another field only": `{"id":"f2","name":"Priority","type":"drop_down","value":0,"type_config":{"options":` + teamOptions + `}}`,
		"field unset":        `{"id":"f1","name":"Team","type":"drop_down","value":null,"type_config":{"options":` + teamOptions + `}}`,
		"unknown option":     `{"id":"f1","name":"Team","type":"drop_down","value":"opt-missing","type_config":{"options":` + teamOptions + `}}`,
		"index out of range": `{"id":"f1","name":"Team","type":"drop_down","value":7,"type_config":{"options":` + teamOptions + `}}`,
		"unexpected shape":   `{"id":"f1","name":"Team","type":"drop_down","value":{"odd":true},"type_config":{"options":` + teamOptions + `}}`,
		"options missing":    `{"id":"f1","name":"Team","type":"drop_down","value":1}`,
	}
	for name, field := range cases {
		t.Run(name, func(t *testing.T) {
			d := newDelegateServer(t, taskWithFields(field))
			team, source, err := d.provider().Delegate(context.Background(), work.WorkItem{ExternalID: "t1"})
			if err != nil || team != "" || source != "" {
				t.Fatalf("Delegate = %q, %q, %v; want no Team and no error", team, source, err)
			}
		})
	}
}

func TestDelegateHonoursAConfiguredFieldName(t *testing.T) {
	d := newDelegateServer(t, taskWithFields(`{"id":"f1","name":"Ploeg Team","type":"drop_down","value":2,"type_config":{"options":`+teamOptions+`}},{"id":"f2","name":"Team","type":"drop_down","value":0,"type_config":{"options":`+teamOptions+`}}`))
	p := d.provider()
	p.TeamField = "ploeg team"
	team, _, err := p.Delegate(context.Background(), work.WorkItem{ExternalID: "t1"})
	if err != nil || team != "gold" {
		t.Fatalf("Delegate = %q, %v; want the configured field's option", team, err)
	}
}

func TestDelegateReusesTheFieldsOfTheRevisionItJustRead(t *testing.T) {
	d := newDelegateServer(t, taskWithFields(`{"id":"f1","name":"Team","type":"drop_down","value":1,"type_config":{"options":`+teamOptions+`}}`))
	p := d.provider()
	item, err := p.FetchItem(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	team, _, err := p.Delegate(context.Background(), item)
	if err != nil || team != "silver" {
		t.Fatalf("Delegate after FetchItem = %q, %v", team, err)
	}
	if n := d.reads.Load(); n != 1 {
		t.Fatalf("the task was read %d times; the Delegate after a read must cost no request", n)
	}
	item.Revision = "stale"
	if _, _, err := p.Delegate(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	if n := d.reads.Load(); n != 2 {
		t.Fatalf("the task was read %d times; another revision must be read again", n)
	}
}

func TestDelegateWithoutCredentialsNamesNoTeam(t *testing.T) {
	p := &Provider{BaseURL: "http://127.0.0.1:0"}
	team, source, err := p.Delegate(context.Background(), work.WorkItem{ExternalID: "t1"})
	if err != nil || team != "" || source != "" {
		t.Fatalf("Delegate = %q, %q, %v", team, source, err)
	}
}

func TestDelegateReportsAnUnreadableTask(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"err":"Team not authorized"}`, http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	p := &Provider{BaseURL: srv.URL, Token: "pk", HC: srv.Client()}
	if _, _, err := p.Delegate(context.Background(), work.WorkItem{ExternalID: "t1"}); err == nil {
		t.Fatal("an unreadable task must be an error, not a silent 'no Team'")
	}
}

func shownComment(t *testing.T, body map[string]any) (string, []map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body["comment"])
	var blocks []map[string]any
	if err := json.Unmarshal(raw, &blocks); err != nil {
		t.Fatal(err)
	}
	plain, _ := body["comment_text"].(string)
	return plain, blocks
}

func TestPostActivityLeadsEveryKindButAResponse(t *testing.T) {
	cases := map[provider.SessionActivityKind]string{
		provider.SessionThought:     "Thought",
		provider.SessionAction:      "Action",
		provider.SessionError:       "Error",
		provider.SessionElicitation: "Question",
		provider.SessionResponse:    "",
	}
	for kind, lead := range cases {
		t.Run(string(kind), func(t *testing.T) {
			d := newDelegateServer(t, taskWithFields(""))
			if err := d.provider().PostActivity(context.Background(), "t1", provider.SessionActivity{Kind: kind, Body: "Reading **the login** flow"}); err != nil {
				t.Fatal(err)
			}
			if len(d.comments) != 1 {
				t.Fatalf("comments = %d, want one", len(d.comments))
			}
			plain, blocks := shownComment(t, d.comments[0])
			if strings.ContainsAny(plain, "*_") {
				t.Errorf("a person reads markup in %q", plain)
			}
			if lead == "" {
				if plain != "Reading the login flow" {
					t.Fatalf("response = %q, want the body as it is", plain)
				}
				return
			}
			if plain != lead+" · Reading the login flow" {
				t.Fatalf("comment = %q, want the lead %q before the body", plain, lead)
			}
			first := blocks[0]
			if first["text"] != lead || first["attributes"].(map[string]any)["italic"] != true {
				t.Errorf("first block = %+v, want the italic lead", first)
			}
		})
	}
}

func TestPostActivityRefusesAnUnknownKindAndAnEmptyBody(t *testing.T) {
	d := newDelegateServer(t, taskWithFields(""))
	p := d.provider()
	if err := p.PostActivity(context.Background(), "t1", provider.SessionActivity{Kind: "musing", Body: "x"}); err == nil {
		t.Error("an unknown kind was posted")
	}
	if err := p.PostActivity(context.Background(), "t1", provider.SessionActivity{Kind: provider.SessionResponse, Body: " \n"}); err == nil {
		t.Error("an empty body was posted")
	}
	if len(d.comments) != 0 {
		t.Fatalf("comments = %d, want none", len(d.comments))
	}
}

func TestSetExternalURLsPostsOneLabelledLinkPerURL(t *testing.T) {
	d := newDelegateServer(t, taskWithFields(""))
	urls := []provider.ExternalURL{
		{Label: "Pull request", URL: "https://forge.example/o/r/pulls/7"},
		{Label: "", URL: "https://example.test/shift/9"},
		{Label: "Nothing", URL: "  "},
	}
	if err := d.provider().SetExternalURLs(context.Background(), "t1", urls); err != nil {
		t.Fatal(err)
	}
	if len(d.comments) != 1 {
		t.Fatalf("comments = %d, want one for the whole list", len(d.comments))
	}
	plain, blocks := shownComment(t, d.comments[0])
	if plain != "Pull request: https://forge.example/o/r/pulls/7\nLink: https://example.test/shift/9" {
		t.Fatalf("comment = %q", plain)
	}
	var linked int
	for _, b := range blocks {
		if attrs, ok := b["attributes"].(map[string]any); ok && attrs["link"] != nil {
			linked++
		}
	}
	if linked != 2 {
		t.Errorf("%d link blocks, want 2", linked)
	}
	if err := d.provider().SetExternalURLs(context.Background(), "t1", nil); err != nil || len(d.comments) != 1 {
		t.Fatalf("an empty list posted a comment: %d, %v", len(d.comments), err)
	}
}

func TestRichCommentRendersItalicsOnlyBetweenWordBoundaries(t *testing.T) {
	plain, blocks := richComment("_Thought_ · needs_human stays, snake_case_too, (_aside_)")
	if plain != "Thought · needs_human stays, snake_case_too, (aside)" {
		t.Fatalf("plain = %q", plain)
	}
	var italics []string
	for _, b := range blocks {
		if b.Attributes["italic"] == true {
			italics = append(italics, b.Text)
		}
	}
	if strings.Join(italics, "|") != "Thought|aside" {
		t.Fatalf("italic blocks = %q", italics)
	}
}

func TestRichCommentReadsTheCoreHTMLRefusalAsText(t *testing.T) {
	plain, blocks := richComment("<p>Ploeg did not queue this task: its Team field names &quot;platinum&quot;.</p><p>Give the task one <code>repo/&lt;target&gt;</code> label.</p>")
	if plain != "Ploeg did not queue this task: its Team field names \"platinum\".\n\nGive the task one repo/<target> label." {
		t.Fatalf("plain = %q", plain)
	}
	var code []string
	for _, b := range blocks {
		if b.Attributes["code"] == true {
			code = append(code, b.Text)
		}
	}
	if strings.Join(code, "|") != "repo/<target>" {
		t.Fatalf("code blocks = %q", code)
	}
}
