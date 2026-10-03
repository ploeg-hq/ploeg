package vikunja

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

const commentPageSize = 50
const commentPageLimit = 10

// BoardStatus reads the task's project, labels and the titles of every
// bucket it sits in across the project's views, through
// GET /tasks/{id}?expand=buckets (ADR-0051).
func (p *Provider) BoardStatus(ctx context.Context, externalID string) (provider.BoardStatus, error) {
	if !p.configured() {
		return provider.BoardStatus{}, errors.New("vikunja: no API credentials configured; cannot read the board")
	}
	var task struct {
		ID        int64  `json:"id"`
		ProjectID int64  `json:"project_id"`
		Created   string `json:"created"`
		Buckets   []struct {
			Title string `json:"title"`
		} `json:"buckets"`
		Labels []struct {
			Title string `json:"title"`
		} `json:"labels"`
	}
	if err := p.do(ctx, http.MethodGet, "/tasks/"+url.PathEscape(externalID)+"?expand=buckets", nil, &task); err != nil {
		return provider.BoardStatus{}, err
	}
	if task.ID == 0 {
		return provider.BoardStatus{}, fmt.Errorf("vikunja: task %s not found", externalID)
	}
	out := provider.BoardStatus{Statuses: []string{}, Labels: []string{}, Created: createdAt(task.Created)}
	if task.ProjectID > 0 {
		out.Scope = fmt.Sprint(task.ProjectID)
	}
	for _, b := range task.Buckets {
		if b.Title != "" {
			out.Statuses = append(out.Statuses, b.Title)
		}
	}
	for _, l := range task.Labels {
		out.Labels = append(out.Labels, l.Title)
	}
	return out, nil
}

func createdAt(raw string) time.Time {
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil || at.Year() <= 1 {
		return time.Time{}
	}
	return at.UTC()
}

// BoardComments reads the task's comments through
// GET /tasks/{id}/comments, page by page.
func (p *Provider) BoardComments(ctx context.Context, externalID string) ([]provider.BoardComment, error) {
	if !p.configured() {
		return nil, errors.New("vikunja: no API credentials configured; cannot read comments")
	}
	var out []provider.BoardComment
	seen := map[int64]bool{}
	for page := 1; page <= commentPageLimit; page++ {
		var comments []struct {
			ID      int64     `json:"id"`
			Comment string    `json:"comment"`
			Created time.Time `json:"created"`
			Author  struct {
				Username string `json:"username"`
			} `json:"author"`
		}
		path := fmt.Sprintf("/tasks/%s/comments?page=%d&per_page=%d", url.PathEscape(externalID), page, commentPageSize)
		if err := p.do(ctx, http.MethodGet, path, nil, &comments); err != nil {
			return nil, err
		}
		fresh := 0
		for _, c := range comments {
			if seen[c.ID] {
				continue
			}
			seen[c.ID] = true
			fresh++
			out = append(out, provider.BoardComment{Text: c.Comment, Author: c.Author.Username, At: c.Created.UTC()})
		}
		if len(comments) < commentPageSize || fresh == 0 {
			break
		}
	}
	return out, nil
}
