package vikunja

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// AssignmentEvent is the Vikunja event that dispatches work to Ploeg.
const AssignmentEvent = "task.assignee.created"

// WebhookPath is the path Ploeg receives Vikunja deliveries on.
const WebhookPath = "/webhooks/tracker/vikunja"

// Webhook is a project webhook as Vikunja reports it.
type Webhook struct {
	ID        int64    `json:"id"`
	TargetURL string   `json:"target_url"`
	Events    []string `json:"events"`
}

// ProjectWebhooks lists a project's webhooks through GET /projects/{id}/webhooks.
func (p *Provider) ProjectWebhooks(ctx context.Context, projectID string) ([]Webhook, error) {
	if !p.configured() {
		return nil, errors.New("vikunja: no API credentials configured; cannot list webhooks")
	}
	var hooks []Webhook
	err := p.do(ctx, http.MethodGet, "/projects/"+url.PathEscape(projectID)+"/webhooks", nil, &hooks)
	return hooks, err
}

// DeliversAssignments reports whether a webhook sends assignment events to
// Ploeg. With an expected URL the target must match it exactly; without one,
// any target on Ploeg's Vikunja webhook path counts.
func DeliversAssignments(h Webhook, expectedURL string) bool {
	if !slices.Contains(h.Events, AssignmentEvent) {
		return false
	}
	target := strings.TrimRight(h.TargetURL, "/")
	if expectedURL != "" {
		return target == strings.TrimRight(expectedURL, "/")
	}
	parsed, err := url.Parse(target)
	return err == nil && strings.HasSuffix(parsed.Path, WebhookPath)
}

// RegisterAssignmentWebhook creates a signed project webhook delivering the
// events Ploeg consumes, through PUT /projects/{id}/webhooks. It refuses to
// register without a signing secret.
func (p *Provider) RegisterAssignmentWebhook(ctx context.Context, projectID, targetURL string) error {
	if !p.configured() {
		return errors.New("vikunja: no API credentials configured; cannot register webhooks")
	}
	if p.Secret == "" {
		return errors.New("vikunja: refusing to register an unsigned webhook; configure the webhook secret")
	}
	if targetURL == "" {
		return errors.New("vikunja: webhook registration needs Ploeg's public webhook URL")
	}
	body := map[string]any{
		"target_url": targetURL,
		"events":     []string{AssignmentEvent, "task.assignee.deleted", "task.updated"},
		"secret":     p.Secret,
	}
	if err := p.do(ctx, http.MethodPut, "/projects/"+url.PathEscape(projectID)+"/webhooks", body, nil); err != nil {
		return fmt.Errorf("vikunja: register webhook for project %s: %w", projectID, err)
	}
	return nil
}

// ProjectUsernames lists the users a project is shared with directly, through
// GET /projects/{id}/users. A user who reaches the project only through a
// Vikunja team or as its owner is not listed.
func (p *Provider) ProjectUsernames(ctx context.Context, projectID string) ([]string, error) {
	if !p.configured() {
		return nil, errors.New("vikunja: no API credentials configured; cannot list project users")
	}
	var users []struct {
		Username string `json:"username"`
	}
	if err := p.do(ctx, http.MethodGet, "/projects/"+url.PathEscape(projectID)+"/users?per_page=250", nil, &users); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(users))
	for _, u := range users {
		out = append(out, u.Username)
	}
	return out, nil
}
