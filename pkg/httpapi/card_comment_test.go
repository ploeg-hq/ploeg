package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/cardimage"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/forgejo"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

type forgeAsset struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

type commentForge struct {
	mu          sync.Mutex
	base        string
	comments    map[int64]string
	order       []int64
	assets      map[int64][]forgeAsset
	nextID      int64
	creates     int
	edits       int
	uploads     int
	deletes     int
	lists       int
	other       []string
	refuseSVG   bool
	failList    bool
	uploadTypes []string
}

var (
	commentPath = regexp.MustCompile(`^/api/v1/repos/webgrip/ploeg/issues/(\d+)/comments$`)
	editPath    = regexp.MustCompile(`^/api/v1/repos/webgrip/ploeg/issues/comments/(\d+)$`)
	assetPath   = regexp.MustCompile(`^/api/v1/repos/webgrip/ploeg/issues/comments/(\d+)/assets(?:/(\d+))?$`)
)

func newCommentForge(t *testing.T) (*commentForge, string) {
	t.Helper()
	f := &commentForge{comments: map[int64]string{}, assets: map[int64][]forgeAsset{}, nextID: 900}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	f.base = srv.URL
	return f, srv.URL
}

func (f *commentForge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	switch {
	case commentPath.MatchString(p) && r.Method == http.MethodGet:
		f.lists++
		if f.failList {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		out := []map[string]any{}
		if r.URL.Query().Get("page") == "1" {
			for _, id := range f.order {
				out = append(out, map[string]any{"id": id, "body": f.comments[id]})
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	case commentPath.MatchString(p) && r.Method == http.MethodPost:
		f.creates++
		var body struct{ Body string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.nextID++
		f.comments[f.nextID] = body.Body
		f.order = append(f.order, f.nextID)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": f.nextID, "body": body.Body})
	case editPath.MatchString(p) && r.Method == http.MethodPatch:
		f.edits++
		id, _ := strconv.ParseInt(editPath.FindStringSubmatch(p)[1], 10, 64)
		var body struct{ Body string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.comments[id] = body.Body
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "body": body.Body})
	case assetPath.MatchString(p):
		m := assetPath.FindStringSubmatch(p)
		id, _ := strconv.ParseInt(m[1], 10, 64)
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(f.assets[id])
		case http.MethodPost:
			f.uploads++
			file, header, err := r.FormFile("attachment")
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			_, _ = io.ReadAll(file)
			f.uploadTypes = append(f.uploadTypes, header.Header.Get("Content-Type"))
			if f.refuseSVG {
				http.Error(w, `{"message":"file type is not allowed"}`, http.StatusUnprocessableEntity)
				return
			}
			f.nextID++
			a := forgeAsset{ID: f.nextID, Name: header.Filename, URL: fmt.Sprintf("%s/attachments/%d", f.base, f.nextID)}
			f.assets[id] = append(f.assets[id], a)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(a)
		case http.MethodDelete:
			f.deletes++
			gone, _ := strconv.ParseInt(m[2], 10, 64)
			var keep []forgeAsset
			for _, a := range f.assets[id] {
				if a.ID != gone {
					keep = append(keep, a)
				}
			}
			f.assets[id] = keep
			w.WriteHeader(http.StatusNoContent)
		}
	default:
		f.other = append(f.other, r.Method+" "+p)
		http.NotFound(w, r)
	}
}

func (f *commentForge) writes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.creates + f.edits + f.uploads + f.deletes
}

func (f *commentForge) cardComments() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, id := range f.order {
		if strings.HasPrefix(f.comments[id], cardimage.CommentMarker) {
			out = append(out, f.comments[id])
		}
	}
	return out
}

type cardClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *cardClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *cardClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func cardCommentServer(t *testing.T, optIn bool) (*Server, *commentForge, *cardClock) {
	t.Helper()
	reset(t)
	forge, url := newCommentForge(t)
	clock := &cardClock{now: time.Now().UTC().Truncate(time.Second)}
	s := &Server{
		Store: testStore, Log: slog.New(slog.DiscardHandler),
		Forges: map[string]provider.ForgeProvider{"forgejo": &forgejo.Provider{BaseURL: url, Secret: "shh",
			Log: slog.New(slog.DiscardHandler)}},
		ForgeBots: []string{"ploeg-bot"},
		CardClock: clock.Now,
	}
	if optIn {
		s.CardRules = map[string]CardRules{"silver": {PRComment: true}}
	}
	return s, forge, clock
}

func cardCommentRecord(t *testing.T, id int64) store.CardComment {
	t.Helper()
	c, ok, err := testStore.CardComment(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("card comment record of %d: ok %v err %v", id, ok, err)
	}
	return c
}

func TestCardComment_OffByDefault(t *testing.T) {
	s, forge, clock := cardCommentServer(t, false)
	id := mergedCardItem(t, "cc-off", "silver", 31, "anna", clock.Now().Add(-time.Hour))
	if err := s.PublishCardComment(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	s.SweepCardComments(context.Background())
	if forge.writes() != 0 || forge.lists != 0 {
		t.Errorf("forge saw %d writes and %d lists for a team that did not opt in", forge.writes(), forge.lists)
	}
	if _, ok, _ := testStore.CardComment(context.Background(), id); ok {
		t.Error("a team that did not opt in got a card comment record")
	}

	s.CardRules = map[string]CardRules{"silver": {HotfixLabels: []string{"hotfix"}}}
	if err := s.PublishCardComment(context.Background(), id); err != nil || forge.writes() != 0 {
		t.Errorf("card rules without prComment posted: err %v, writes %d", err, forge.writes())
	}
}

func TestCardComment_PostsOnceAndEditsInPlace(t *testing.T) {
	s, forge, clock := cardCommentServer(t, true)
	ctx := context.Background()
	id := mergedCardItem(t, "cc-once", "silver", 32, "anna", clock.Now().Add(-time.Hour))

	if err := s.PublishCardComment(ctx, id); err != nil {
		t.Fatal(err)
	}
	cards := forge.cardComments()
	if len(cards) != 1 || forge.creates != 1 || forge.uploads != 1 || forge.edits != 1 {
		t.Fatalf("after the merge: %d card comments, %d creates, %d uploads, %d edits", len(cards), forge.creates, forge.uploads, forge.edits)
	}
	if forge.uploadTypes[0] != cardimage.ContentType {
		t.Errorf("uploaded %q", forge.uploadTypes[0])
	}
	if !strings.Contains(cards[0], "### Run card · Merged") || !strings.Contains(cards[0], "![Run card: card cc-once]("+forge.base+"/attachments/") ||
		!strings.Contains(cards[0], "| Steward | anna · Merged the pull request |") {
		t.Errorf("card comment =\n%s", cards[0])
	}
	rec := cardCommentRecord(t, id)
	if rec.Moment != "merged:32;finish:matte" || !rec.Image || rec.CommentID == nil || rec.PublishedAt == nil {
		t.Errorf("record = %+v", rec)
	}

	writes := forge.writes()
	if err := s.PublishCardComment(ctx, id); err != nil {
		t.Fatal(err)
	}
	if forge.writes() != writes {
		t.Errorf("publishing the same moment again wrote to the forge: %d → %d", writes, forge.writes())
	}

	if _, err := testPool.Exec(ctx, `DELETE FROM card_comments WHERE work_item_id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if err := s.PublishCardComment(ctx, id); err != nil {
		t.Fatal(err)
	}
	if n := len(forge.cardComments()); n != 1 || forge.creates != 1 {
		t.Errorf("a lost record posted a second card: %d card comments, %d creates", n, forge.creates)
	}

	clock.add(7 * 24 * time.Hour)
	if err := s.PublishCardComment(ctx, id); err != nil {
		t.Fatal(err)
	}
	cards = forge.cardComments()
	if len(cards) != 1 || forge.creates != 1 || !strings.Contains(cards[0], "### Run card · Foil finish") || !strings.Contains(cards[0], "| Days live | Day 7 · Foil finish") {
		t.Errorf("finish level up: %d card comments, %d creates:\n%s", len(cards), forge.creates, strings.Join(cards, "\n"))
	}
	if got := cardCommentRecord(t, id).Moment; got != "merged:32;finish:foil" {
		t.Errorf("moment = %q", got)
	}
	forge.mu.Lock()
	defer forge.mu.Unlock()
	for comment, assets := range forge.assets {
		if len(assets) != 1 {
			t.Errorf("comment %d keeps %d card images; want the newest only", comment, len(assets))
		}
	}
}

func TestCardComment_RefusedImageLeavesTheTable(t *testing.T) {
	s, forge, clock := cardCommentServer(t, true)
	forge.refuseSVG = true
	id := mergedCardItem(t, "cc-refused", "silver", 33, "anna", clock.Now().Add(-time.Hour))
	if err := s.PublishCardComment(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	cards := forge.cardComments()
	if len(cards) != 1 || forge.uploads != 1 || forge.edits != 0 {
		t.Fatalf("%d card comments, %d uploads, %d edits", len(cards), forge.uploads, forge.edits)
	}
	if strings.Contains(cards[0], "![") || !strings.Contains(cards[0], "| State | Merged |") {
		t.Errorf("card comment without an image =\n%s", cards[0])
	}
	if rec := cardCommentRecord(t, id); rec.Image || rec.Moment != "merged:33;finish:matte" {
		t.Errorf("record = %+v", rec)
	}
}

func TestCardComment_NeverPostsBlind(t *testing.T) {
	s, forge, clock := cardCommentServer(t, true)
	forge.failList = true
	id := mergedCardItem(t, "cc-blind", "silver", 34, "anna", clock.Now().Add(-time.Hour))
	if err := s.PublishCardComment(context.Background(), id); err == nil {
		t.Fatal("a failed comment list was not reported")
	}
	if forge.writes() != 0 {
		t.Errorf("posted %d writes without seeing the thread", forge.writes())
	}
	if _, ok, _ := testStore.CardComment(context.Background(), id); ok {
		t.Error("an unposted moment was recorded")
	}
}

func TestCardComment_SweepFindsRecentMergesAndSkipsOldOnes(t *testing.T) {
	s, forge, clock := cardCommentServer(t, true)
	recent := mergedCardItem(t, "cc-recent", "silver", 35, "anna", clock.Now().Add(-2*24*time.Hour))
	old := mergedCardItem(t, "cc-old", "silver", 36, "anna", clock.Now().Add(-30*24*time.Hour))
	other := mergedCardItem(t, "cc-gold", "gold", 37, "anna", clock.Now().Add(-time.Hour))

	s.SweepCardComments(context.Background())
	if n := len(forge.cardComments()); n != 1 {
		t.Fatalf("sweep posted %d card comments; want the recent merge only", n)
	}
	if rec := cardCommentRecord(t, recent); rec.Moment == "" {
		t.Errorf("recent merge record = %+v", rec)
	}
	for _, id := range []int64{old, other} {
		if _, ok, _ := testStore.CardComment(context.Background(), id); ok {
			t.Errorf("work item %d got a card comment record", id)
		}
	}

	writes := forge.writes()
	s.SweepCardComments(context.Background())
	if forge.writes() != writes {
		t.Error("a second sweep within the hour wrote to the forge")
	}
}

func TestCardComment_OnePublisherAtATime(t *testing.T) {
	s, forge, clock := cardCommentServer(t, true)
	ctx := context.Background()
	id := mergedCardItem(t, "cc-lock", "silver", 38, "anna", clock.Now().Add(-time.Hour))
	unlock, held, err := testStore.LockCardComment(ctx, id)
	if err != nil || !held {
		t.Fatalf("lock: held %v err %v", held, err)
	}
	if err := s.PublishCardComment(ctx, id); err != nil {
		t.Fatal(err)
	}
	if forge.writes() != 0 || forge.lists != 0 {
		t.Errorf("a held card comment was published: %d writes, %d lists", forge.writes(), forge.lists)
	}
	unlock()
	if err := s.PublishCardComment(ctx, id); err != nil || len(forge.cardComments()) != 1 {
		t.Errorf("after unlock: err %v, %d card comments", err, len(forge.cardComments()))
	}
}

func TestCardComment_MergeWebhookPostsTheCard(t *testing.T) {
	s, forge, clock := cardCommentServer(t, true)
	clock.now = time.Date(2026, 10, 1, 10, 30, 0, 0, time.UTC)
	factsItem(t)
	if code := forgePostEvent(t, s.Handler(), "shh", "pull_request", "merge-card-1", factsMerge()); code != http.StatusAccepted {
		t.Fatalf("webhook returned %d", code)
	}
	s.cardWork.Wait()
	cards := forge.cardComments()
	if len(cards) != 1 || !strings.Contains(cards[0], "### Run card · Merged") || !strings.Contains(cards[0], "| Steward | ryan · Merged the pull request |") {
		t.Fatalf("card comments after the merge webhook = %q", cards)
	}
}

func TestCardsDisabled_StopsTheCardSweepsAndPublisher(t *testing.T) {
	s, forge, clock := cardCommentServer(t, true)
	s.CardsDisabled = true
	ctx := context.Background()
	id := mergedCardItem(t, "cards-off", "silver", 33, "anna", clock.Now().Add(-time.Hour))

	s.SweepCardComments(ctx)
	s.SweepCardRarity(ctx)
	if err := s.PublishCardComment(ctx, id); err != nil {
		t.Fatal(err)
	}
	s.publishMergedCard(ctx, s.Forges["forgejo"], provider.ForgeEvent{Kind: provider.ForgePRMerged, Repo: "webgrip/ploeg", PR: 33})
	s.cardWork.Wait()
	if forge.writes() != 0 || forge.lists != 0 {
		t.Errorf("forge saw %d writes and %d lists with cards disabled", forge.writes(), forge.lists)
	}
	if _, ok, _ := testStore.CardComment(ctx, id); ok {
		t.Error("cards disabled still recorded a card comment")
	}
	var rarity int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM card_rarity WHERE work_item_id = $1`, id).Scan(&rarity); err != nil || rarity != 0 {
		t.Errorf("cards disabled still checked rarity: %d %v", rarity, err)
	}

	s.CardsDisabled = false
	if err := s.PublishCardComment(ctx, id); err != nil || len(forge.cardComments()) != 1 {
		t.Errorf("cards enabled again: err %v, %d card comments", err, len(forge.cardComments()))
	}
}
