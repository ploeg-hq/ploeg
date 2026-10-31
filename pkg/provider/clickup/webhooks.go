package clickup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

// WebhookPath is the path Ploeg receives ClickUp deliveries on.
const WebhookPath = "/webhooks/tracker/clickup"

// DispatchEvents are the webhook events a ClickUp webhook must deliver for
// assignments to dispatch and status changes to reach Ploeg.
var DispatchEvents = []string{"taskAssigneeUpdated", "taskStatusUpdated", "taskUpdated"}

// registeredEvents are the events RegisterWebhook subscribes to: the
// dispatch events plus every other event ParseWebhook reads.
var registeredEvents = []string{"taskAssigneeUpdated", "taskStatusUpdated", "taskUpdated", "taskPriorityUpdated"}

// webhookSuspended is the health status ClickUp gives a webhook it stopped
// delivering after repeated failures.
const webhookSuspended = "suspended"

// Webhook is a Workspace webhook as ClickUp reports it. Its signing secret
// is deliberately not decoded.
type Webhook struct {
	ID       string   `json:"id"`
	Endpoint string   `json:"endpoint"`
	Events   []string `json:"events"`
	TaskID   flexID   `json:"task_id"`
	ListID   flexID   `json:"list_id"`
	FolderID flexID   `json:"folder_id"`
	SpaceID  flexID   `json:"space_id"`
	Health   struct {
		Status    string `json:"status"`
		FailCount int    `json:"fail_count"`
	} `json:"health"`
}

// flexID decodes a ClickUp location id that arrives as a string, a number
// or null.
type flexID string

func (f *flexID) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if bytes.Equal(b, []byte("null")) {
		*f = ""
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*f = flexID(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("clickup: location id %s is neither a string nor a number", b)
	}
	*f = flexID(n.String())
	return nil
}

// ListLocation is a List with the Folder and Space that hold it.
type ListLocation struct {
	ID       string
	Name     string
	FolderID string
	SpaceID  string
}

// WorkspaceWebhooks lists the Workspace's webhooks the token's user created,
// through GET /team/{workspace}/webhook.
func (p *Provider) WorkspaceWebhooks(ctx context.Context, workspaceID string) ([]Webhook, error) {
	if !p.configured() {
		return nil, errors.New("clickup: no API credentials configured; cannot list webhooks")
	}
	if workspaceID == "" {
		return nil, errors.New("clickup: a Workspace id is required to list webhooks")
	}
	var body struct {
		Webhooks []Webhook `json:"webhooks"`
	}
	if err := p.do(ctx, http.MethodGet, "/team/"+url.PathEscape(workspaceID)+"/webhook", nil, &body); err != nil {
		return nil, err
	}
	return body.Webhooks, nil
}

// List reads a List's name and location through GET /list/{id}.
func (p *Provider) List(ctx context.Context, listID string) (ListLocation, error) {
	if !p.configured() {
		return ListLocation{}, errors.New("clickup: no API credentials configured; cannot read lists")
	}
	var body struct {
		ID     flexID `json:"id"`
		Name   string `json:"name"`
		Folder struct {
			ID flexID `json:"id"`
		} `json:"folder"`
		Space struct {
			ID flexID `json:"id"`
		} `json:"space"`
	}
	if err := p.do(ctx, http.MethodGet, "/list/"+url.PathEscape(listID), nil, &body); err != nil {
		return ListLocation{}, err
	}
	if body.ID == "" {
		return ListLocation{}, fmt.Errorf("clickup: list %s not found", listID)
	}
	return ListLocation{ID: string(body.ID), Name: body.Name, FolderID: string(body.Folder.ID), SpaceID: string(body.Space.ID)}, nil
}

// ListMembers lists the usernames of the people who can see a List, through
// GET /list/{id}/member.
func (p *Provider) ListMembers(ctx context.Context, listID string) ([]string, error) {
	if !p.configured() {
		return nil, errors.New("clickup: no API credentials configured; cannot list members")
	}
	var body struct {
		Members []struct {
			Username string `json:"username"`
			Email    string `json:"email"`
		} `json:"members"`
	}
	if err := p.do(ctx, http.MethodGet, "/list/"+url.PathEscape(listID)+"/member", nil, &body); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(body.Members))
	for _, m := range body.Members {
		if name := firstNonEmpty(m.Username, m.Email); name != "" {
			out = append(out, name)
		}
	}
	return out, nil
}

// Covers reports whether the webhook's location includes the List. ClickUp
// filters on the most specific location a webhook names, and a webhook that
// names none covers the whole Workspace.
func (h Webhook) Covers(l ListLocation) bool {
	switch {
	case h.TaskID != "":
		return false
	case h.ListID != "":
		return string(h.ListID) == l.ID
	case h.FolderID != "":
		return string(h.FolderID) == l.FolderID
	case h.SpaceID != "":
		return string(h.SpaceID) == l.SpaceID
	}
	return true
}

// DeliversDispatchEvents reports whether a webhook sends every dispatch
// event to Ploeg and ClickUp has not suspended it. With an expected URL the
// endpoint must match it exactly; without one, any endpoint on Ploeg's
// ClickUp webhook path counts.
func DeliversDispatchEvents(h Webhook, expectedURL string) bool {
	if strings.EqualFold(h.Health.Status, webhookSuspended) {
		return false
	}
	if !slices.Contains(h.Events, "*") {
		for _, event := range DispatchEvents {
			if !slices.Contains(h.Events, event) {
				return false
			}
		}
	}
	endpoint := strings.TrimRight(h.Endpoint, "/")
	if expectedURL != "" {
		return endpoint == strings.TrimRight(expectedURL, "/")
	}
	parsed, err := url.Parse(endpoint)
	return err == nil && strings.HasSuffix(parsed.Path, WebhookPath)
}

// CheckWebhooks reports, for each List, whether a Workspace webhook that
// covers it delivers every dispatch event to endpoint, and who can see the
// List (ADR-0183). It lists the Workspace's webhooks once and reads each List
// for its Folder and Space. ClickUp lists only the webhooks the token's user
// created.
func (p *Provider) CheckWebhooks(ctx context.Context, lists []string, endpoint string) []provider.ScopeWebhook {
	hooks, hooksErr := p.WorkspaceWebhooks(ctx, p.Workspace)
	out := make([]provider.ScopeWebhook, 0, len(lists))
	for _, id := range lists {
		found := provider.ScopeWebhook{Scope: id, Webhook: provider.WebhookMissing}
		if members, err := p.ListMembers(ctx, id); err == nil {
			found.Members = members
		}
		loc, err := p.List(ctx, id)
		switch {
		case err != nil:
			found.Webhook, found.Err = provider.WebhookUnreadable, err
		case hooksErr != nil:
			found.Webhook, found.Err = provider.WebhookUnreadable, hooksErr
		case covered(hooks, loc, endpoint):
			found.Webhook = provider.WebhookPresent
		}
		out = append(out, found)
	}
	return out
}

func covered(hooks []Webhook, loc ListLocation, endpoint string) bool {
	for _, h := range hooks {
		if h.Covers(loc) && DeliversDispatchEvents(h, endpoint) {
			return true
		}
	}
	return false
}

// RegisterWebhook creates a Workspace-wide webhook delivering the events
// Ploeg consumes, through POST /team/{workspace}/webhook, and hands its
// signing secret to secrets. It refuses without a sink, because ClickUp
// returns the secret only in the create response. When the sink refuses the
// secret, the new webhook is deleted again.
func (p *Provider) RegisterWebhook(ctx context.Context, targetURL string, sink provider.WebhookSecretSink) (string, error) {
	workspaceID := p.Workspace
	if !p.configured() {
		return "", errors.New("clickup: no API credentials configured; cannot register webhooks")
	}
	if sink == nil {
		return "", errors.New("clickup: refusing to register a webhook: ClickUp hands its signing secret over at creation and no secret sink is configured to keep it")
	}
	if workspaceID == "" || targetURL == "" {
		return "", errors.New("clickup: webhook registration needs the Workspace id and Ploeg's public webhook URL")
	}
	var created struct {
		ID      string `json:"id"`
		Webhook struct {
			ID     string `json:"id"`
			Secret string `json:"secret"`
		} `json:"webhook"`
	}
	body := map[string]any{"endpoint": targetURL, "events": registeredEvents}
	if err := p.do(ctx, http.MethodPost, "/team/"+url.PathEscape(workspaceID)+"/webhook", body, &created); err != nil {
		return "", fmt.Errorf("clickup: register webhook for workspace %s: %w", workspaceID, err)
	}
	id := firstNonEmpty(created.Webhook.ID, created.ID)
	if id == "" {
		return "", errors.New("clickup: the create-webhook response named no webhook")
	}
	var keepErr error
	if created.Webhook.Secret == "" {
		keepErr = errors.New("clickup: the create-webhook response carried no signing secret")
	} else {
		keepErr = sink.StoreWebhookSecret(ctx, id, created.Webhook.Secret)
	}
	if keepErr != nil {
		if err := p.do(context.WithoutCancel(ctx), http.MethodDelete, "/webhook/"+url.PathEscape(id), nil, nil); err != nil {
			return "", errors.Join(fmt.Errorf("clickup: webhook %s not kept: %w", id, keepErr), fmt.Errorf("clickup: delete webhook %s: %w", id, err))
		}
		return "", fmt.Errorf("clickup: webhook %s not kept and deleted again: %w", id, keepErr)
	}
	return id, nil
}
