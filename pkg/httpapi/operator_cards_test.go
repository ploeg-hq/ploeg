package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type cardsBody struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Cards         []store.OperatorCard `json:"cards"`
	NextBefore    *string              `json:"nextBefore"`
}

func mergedCardItem(t *testing.T, externalID, team string, number int, mergedBy string, at time.Time) int64 {
	t.Helper()
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: externalID, Team: team, Title: "card " + externalID,
		Target: &work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "ploeg", BaseBranch: "development"}})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := testStore.RecordPullRequestFacts(ctx, store.PullRequestFacts{Forge: "forgejo", Repo: "webgrip/ploeg", Number: number,
		WorkItemID: id, State: "merged", MergedAt: &at, MergedBy: mergedBy, HeadSHA: fmt.Sprintf("%040d", number)}); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE pull_requests SET first_seen_at = $2 WHERE work_item_id = $1`, id, at); err != nil {
		t.Fatal(err)
	}
	return id
}

func cardsGET(t *testing.T, s *Server, token, query string) cardsBody {
	t.Helper()
	var body cardsBody
	if err := json.Unmarshal(operatorSchemaGET(t, s, token, "cards?"+query), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestOperatorCards_EndpointMatchesTheSchemaAndPages(t *testing.T) {
	s, token := cardServer(t, &fakeForgejo{})
	s.OperatorConfig.CardStyles = map[string]store.CardStyle{"webgrip/ploeg": {Skin: "foil"}}
	now := time.Now().UTC().Truncate(time.Second)
	var want []string
	for i := 1; i <= 3; i++ {
		id := mergedCardItem(t, "list-"+strconv.Itoa(i), "silver", i, "Anna", now.Add(-time.Duration(4-i)*time.Hour))
		want = append([]string{strconv.FormatInt(id, 10)}, want...)
	}
	mergedCardItem(t, "list-gold", "gold", 9, "anna", now)

	empty := cardsGET(t, s, token, "member=nobody")
	if empty.SchemaVersion != 1 || len(empty.Cards) != 0 || empty.NextBefore != nil {
		t.Errorf("empty list = %+v", empty)
	}

	first := cardsGET(t, s, token, "member=anna&limit=2")
	if len(first.Cards) != 2 || first.NextBefore == nil || first.Cards[0].WorkItemID != want[0] || first.Cards[1].WorkItemID != want[1] {
		t.Fatalf("first page = %+v", first)
	}
	c := first.Cards[0]
	if c.Style.Skin != "foil" || c.Steward == nil || c.Steward.Name != "Anna" || c.State != "merged" || c.Team != "silver" {
		t.Errorf("listed card = %+v; want the full card with its style", c)
	}
	second := cardsGET(t, s, token, "member=anna&limit=2&before="+url.QueryEscape(*first.NextBefore))
	if len(second.Cards) != 1 || second.Cards[0].WorkItemID != want[2] || second.NextBefore != nil {
		t.Fatalf("second page = %+v", second)
	}

	since := url.QueryEscape(now.Add(-150 * time.Minute).Format(time.RFC3339))
	if got := cardsGET(t, s, token, "member=nobody&member=ANNA&since="+since); len(got.Cards) != 2 {
		t.Errorf("since = %d cards; want the two active within 150 minutes", len(got.Cards))
	}
	for _, card := range cardsGET(t, s, token, "member=anna&limit=50").Cards {
		if card.Team != "silver" {
			t.Errorf("a silver consumer saw card %s of team %s", card.WorkItemID, card.Team)
		}
	}
	if got := cardsGET(t, s, token, "member=anna&team=silver"); len(got.Cards) != 3 {
		t.Errorf("team=silver = %d cards", len(got.Cards))
	}
}

func TestOperatorCards_RejectsBadQueries(t *testing.T) {
	s, token := cardServer(t, &fakeForgejo{})
	many := strings.Repeat("member=a&", 21)
	for _, tc := range []struct {
		query  string
		status int
	}{
		{"", 400},
		{"team=silver", 400},
		{"member=", 400},
		{many[:len(many)-1], 400},
		{"member=" + strings.Repeat("a", 257), 400},
		{"member=a%0Ab", 400},
		{"member=anna&limit=51", 400},
		{"member=anna&limit=0", 400},
		{"member=anna&limit=x", 400},
		{"member=anna&since=yesterday", 400},
		{"member=anna&before=42", 400},
		{"member=anna&before=c1.YS4x", 400},
		{"member=anna&team=silver&team=silver", 400},
		{"member=anna&order=asc", 400},
		{"member=anna&team=bad%20team", 400},
		{"member=anna&team=gold", 403},
		{"member=anna&limit=50", 200},
	} {
		r := httptest.NewRequest("GET", "/api/v1/operator/cards?"+tc.query, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Errorf("%q: %d %s; want %d", tc.query, w.Code, w.Body, tc.status)
		}
	}
	r := httptest.NewRequest("GET", "/api/v1/operator/cards?member=anna", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 401 {
		t.Errorf("unauthenticated card list: %d", w.Code)
	}
	r = httptest.NewRequest("POST", "/api/v1/operator/cards?member=anna", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 405 {
		t.Errorf("POST card list: %d", w.Code)
	}
}
