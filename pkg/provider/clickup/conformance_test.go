package clickup

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/trackertest"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestClickUpPassesTheTrackerConformanceSuite(t *testing.T) {
	trackertest.Run(t, trackertest.Fixture{
		New: func(baseURL, secret string, configured bool, statuses map[work.State]string) provider.TrackerProvider {
			p := &Provider{BaseURL: baseURL, Secret: secret, DefaultTeam: "default", TeamMap: map[string]string{"builder": "silver"}, Statuses: statuses}
			if configured {
				p.Token = "pk_conformance"
			}
			return p
		},
		Server:        fakeClickUp,
		Assignment:    signedAssignment,
		StatusMapping: true,
	})
}

func TestClickUpDeclaresItsTrackerCapabilities(t *testing.T) {
	got := strings.Join(provider.TrackerCapabilities(&Provider{}), ",")
	want := "execution_reader,delivery_identifier,board_reader,task_linker,relation_reader,dependency_reader,webhook_checker,webhook_registrar,reference_namer"
	if got != want {
		t.Fatalf("capabilities = %s, want %s", got, want)
	}
}

func signedAssignment(secret, taskID, assignee string) *http.Request {
	body := fmt.Sprintf(`{"event":"taskAssigneeUpdated","task_id":%q,"history_items":[{"id":"h1","field":"assignee_add","date":"1759310400000","after":{"username":%q}}]}`, taskID, assignee)
	r := httptest.NewRequest(http.MethodPost, "/webhooks/tracker/clickup", strings.NewReader(body))
	r.Header.Set("X-Signature", sign(secret, []byte(body)))
	return r
}

// fakeClickUp serves the slice of ClickUp's API the adapter uses, over w.
func fakeClickUp(w *trackertest.World) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			http.Error(rw, `{"err":"Token invalid"}`, http.StatusUnauthorized)
			return
		}
		rest, ok := strings.CutPrefix(r.URL.Path, "/task/")
		if !ok {
			http.NotFound(rw, r)
			return
		}
		id, sub, _ := strings.Cut(rest, "/")
		switch {
		case r.Method == http.MethodGet && sub == "":
			task, ok := w.Read(id)
			if !ok {
				http.Error(rw, `{"err":"Task not found"}`, http.StatusNotFound)
				return
			}
			statusType := "custom"
			if task.Closed {
				statusType = "closed"
			}
			deps := []map[string]any{}
			for _, d := range task.DependsOn {
				deps = append(deps, map[string]any{"task_id": task.ID, "depends_on": d, "type": 1})
			}
			out := map[string]any{"id": task.ID, "name": task.Title, "url": "https://app.clickup.com/t/" + task.ID,
				"description": strings.NewReplacer("#", "", "*", "").Replace(task.Body),
				"status":      map[string]any{"status": task.Status, "type": statusType},
				"list":        map[string]any{"id": task.Scope}, "dependencies": deps}
			if r.URL.Query().Get("include_markdown_description") == "true" {
				out["markdown_description"] = task.Body
			}
			_ = json.NewEncoder(rw).Encode(out)
		case r.Method == http.MethodPost && sub == "comment":
			var body struct {
				CommentText string `json:"comment_text"`
				Comment     []struct {
					Text string `json:"text"`
				} `json:"comment"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(rw, err.Error(), http.StatusBadRequest)
				return
			}
			shown := body.CommentText
			if len(body.Comment) > 0 {
				var b strings.Builder
				for _, block := range body.Comment {
					b.WriteString(block.Text)
				}
				shown = strings.TrimRight(b.String(), "\n")
			}
			if !w.Update(id, func(t *trackertest.Task) { t.Comments = append(t.Comments, shown) }) {
				http.NotFound(rw, r)
				return
			}
			fmt.Fprint(rw, `{"id":"c1"}`)
		case r.Method == http.MethodPut && sub == "":
			var body struct {
				Status string `json:"status"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Status == "" {
				http.Error(rw, `{"err":"Status not found"}`, http.StatusBadRequest)
				return
			}
			w.Update(id, func(t *trackertest.Task) { t.Status = body.Status })
			fmt.Fprint(rw, `{}`)
		default:
			http.NotFound(rw, r)
		}
	})
}
