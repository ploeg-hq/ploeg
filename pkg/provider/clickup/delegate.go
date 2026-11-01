package clickup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// DefaultTeamField is the name of the dropdown custom field a person sets
// to name the Team for a task, when the configuration names no other
// (ADR-0187).
const DefaultTeamField = "Team"

const fieldCacheEntries = 256

type customField struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Type       string          `json:"type"`
	Value      json.RawMessage `json:"value"`
	TypeConfig struct {
		Options []customFieldOption `json:"options"`
	} `json:"type_config"`
}

type customFieldOption struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	OrderIndex json.RawMessage `json:"orderindex"`
	Color      string          `json:"color"`
}

func (f customField) selectedOption() string {
	value := strings.TrimSpace(string(f.Value))
	if value == "" || value == "null" {
		return ""
	}
	var index float64
	if json.Unmarshal(f.Value, &index) == nil {
		return f.optionAt(strconv.FormatInt(int64(index), 10))
	}
	var id string
	if json.Unmarshal(f.Value, &id) != nil {
		return ""
	}
	for _, option := range f.TypeConfig.Options {
		if option.ID == id {
			return option.Name
		}
	}
	return f.optionAt(id)
}

func (f customField) optionAt(index string) string {
	for _, option := range f.TypeConfig.Options {
		if strings.Trim(string(option.OrderIndex), `"`) == index {
			return option.Name
		}
	}
	return ""
}

func (t task) fieldOptions() map[string]string {
	out := map[string]string{}
	for _, f := range t.CustomFields {
		if f.Name == "" {
			continue
		}
		out[f.Name] = f.selectedOption()
	}
	return out
}

type cachedFields struct {
	revision string
	fields   map[string]string
}

func (p *Provider) teamField() string {
	if name := strings.TrimSpace(p.TeamField); name != "" {
		return name
	}
	return DefaultTeamField
}

func (p *Provider) rememberFields(t task) {
	p.fieldsMu.Lock()
	defer p.fieldsMu.Unlock()
	if p.fields == nil || len(p.fields) >= fieldCacheEntries {
		p.fields = map[string]cachedFields{}
	}
	p.fields[t.ID] = cachedFields{revision: t.DateUpdated, fields: t.fieldOptions()}
}

func (p *Provider) cachedFields(item work.WorkItem) (map[string]string, bool) {
	p.fieldsMu.Lock()
	defer p.fieldsMu.Unlock()
	entry, ok := p.fields[item.ExternalID]
	if !ok || item.Revision == "" || entry.revision != item.Revision {
		return nil, false
	}
	return entry.fields, true
}

// Delegate names the Team the task's Team field (TeamField, default
// DefaultTeamField) is set to, lower-cased, with the source
// provider.DelegateSourceField; an unset or absent field names no Team
// (ADR-0187). The fields the last read of this revision returned are
// reused, so the Delegate call that follows a FetchItem costs no request;
// any other item is read again. An unconfigured adapter names no Team.
func (p *Provider) Delegate(ctx context.Context, item work.WorkItem) (string, string, error) {
	if !p.configured() {
		p.log().Debug("clickup delegate skipped (no API credentials)", "external_id", item.ExternalID)
		return "", "", nil
	}
	if item.ExternalID == "" {
		return "", "", errors.New("clickup: a delegate needs the task id")
	}
	fields, ok := p.cachedFields(item)
	if !ok {
		t, err := p.readTask(ctx, item.ExternalID)
		if err != nil {
			return "", "", err
		}
		p.rememberFields(t)
		fields = t.fieldOptions()
	}
	want := p.teamField()
	for name, option := range fields {
		if !strings.EqualFold(strings.TrimSpace(name), want) {
			continue
		}
		team := strings.ToLower(strings.TrimSpace(option))
		if team == "" {
			return "", "", nil
		}
		return team, provider.DelegateSourceField, nil
	}
	return "", "", nil
}

var activityLeads = map[provider.SessionActivityKind]string{
	provider.SessionThought:     "Thought",
	provider.SessionAction:      "Action",
	provider.SessionError:       "Error",
	provider.SessionElicitation: "Question",
	provider.SessionResponse:    "",
}

// PostActivity reports one activity as a comment on the task: a response
// as its Markdown body, every other kind with an italic lead naming the
// kind (ADR-0187). It refuses a kind it has no lead for.
func (p *Provider) PostActivity(ctx context.Context, externalID string, activity provider.SessionActivity) error {
	lead, known := activityLeads[activity.Kind]
	if !known {
		return fmt.Errorf("clickup: unknown activity kind %q", activity.Kind)
	}
	body := strings.TrimSpace(activity.Body)
	if body == "" {
		return errors.New("clickup: an activity needs a body")
	}
	if lead != "" {
		body = "_" + lead + "_ · " + body
	}
	return p.Comment(ctx, externalID, body)
}

// SetExternalURLs posts one comment with a bold label and link per URL
// (ADR-0187). Nothing is posted for an empty list.
func (p *Provider) SetExternalURLs(ctx context.Context, externalID string, urls []provider.ExternalURL) error {
	lines := make([]string, 0, len(urls))
	for _, u := range urls {
		if strings.TrimSpace(u.URL) == "" {
			continue
		}
		label := strings.TrimSpace(u.Label)
		if label == "" {
			label = "Link"
		}
		lines = append(lines, "**"+label+":** "+strings.TrimSpace(u.URL))
	}
	if len(lines) == 0 {
		return nil
	}
	return p.Comment(ctx, externalID, strings.Join(lines, "\n"))
}
