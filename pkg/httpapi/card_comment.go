package httpapi

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/cardimage"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

const (
	cardCommentTimeout = 30 * time.Second
	cardCommentBatch   = 25
	cardCommentRecheck = time.Hour
	cardCommentRecent  = 7 * 24 * time.Hour
)

func (s *Server) cardClock() time.Time {
	if s.CardClock != nil {
		return s.CardClock()
	}
	return time.Now()
}

func (s *Server) cardCommentTeams() []string {
	var out []string
	for team, rules := range s.CardRules {
		if rules.PRComment {
			out = append(out, team)
		}
	}
	sort.Strings(out)
	return out
}

// SweepCardComments assembles the cards of up to 25 opted-in Work Items that
// were not checked in the last hour and posts each new moment (ADR-0055):
// the cards with a card comment record, and those with a play merged in the
// last seven days. It finds a release, a finish climbed and a mend; the
// forge webhook usually posts a merge first. Failures are logged.
func (s *Server) SweepCardComments(ctx context.Context) {
	if s.CardsDisabled {
		return
	}
	teams := s.cardCommentTeams()
	if len(teams) == 0 || len(s.Forges) == 0 {
		return
	}
	now := s.cardClock()
	ids, err := s.Store.CardCommentCandidates(ctx, teams, now.Add(-cardCommentRecent), now.Add(-cardCommentRecheck), cardCommentBatch)
	if err != nil {
		s.Log.Error("card comment sweep failed", "err", err)
		return
	}
	for _, id := range ids {
		if err := s.PublishCardComment(ctx, id); err != nil {
			s.Log.Warn("card comment not published", "work_item", id, "err", err)
		}
	}
}

func (s *Server) publishMergedCard(ctx context.Context, fp provider.ForgeProvider, ev provider.ForgeEvent) {
	if s.CardsDisabled || len(s.cardCommentTeams()) == 0 {
		return
	}
	id, team, ok, err := s.Store.PlayWorkItem(ctx, fp.Name(), ev.Repo, ev.PR)
	if err != nil || !ok || !s.CardRules[team].PRComment {
		return
	}
	s.cardWork.Add(1)
	go func() {
		defer s.cardWork.Done()
		if err := s.PublishCardComment(context.WithoutCancel(ctx), id); err != nil {
			s.Log.Warn("card comment not published", "work_item", id, "provider", fp.Name(), "repo", ev.Repo, "pr", ev.PR, "err", err)
		}
	}()
}

// PublishCardComment posts or edits the card comment of Work Item id when
// its card reached a moment the comment does not show yet (ADR-0055). The
// comment goes on the latest merged play and is found there by
// cardimage.CommentMarker. The image is attached where the forge accepts it;
// otherwise the comment carries the summary table alone. A Work Item of a
// team that did not opt in, or one another publisher holds, is skipped.
func (s *Server) PublishCardComment(ctx context.Context, id int64) error {
	if s.CardsDisabled {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, cardCommentTimeout)
	defer cancel()
	unlock, held, err := s.Store.LockCardComment(ctx, id)
	if err != nil || !held {
		return err
	}
	defer unlock()

	now := s.cardClock()
	card, err := s.Store.OperatorCard(ctx, id, nil, store.CardOptions{Bots: s.ForgeBots,
		ReleaseEnvironments: s.OperatorConfig.ReleaseEnvironments, HotfixLabels: s.cardHotfixLabels(), Now: now})
	if err != nil {
		return err
	}
	if !s.CardRules[card.Team].PRComment {
		return nil
	}
	previous, _, err := s.Store.CardComment(ctx, id)
	if err != nil {
		return err
	}
	moment := cardimage.MomentOf(card, now)
	if moment.Key == "" || moment.Key == previous.Moment {
		return s.Store.MarkCardCommentChecked(ctx, id, now)
	}
	play, ok, err := s.Store.CardCommentPlay(ctx, id, moment.Play)
	if err != nil {
		return err
	}
	fp := s.Forges[play.Forge]
	if !ok || fp == nil {
		s.Log.Info("card comment not published: no forge provider for the play", "work_item", id, "forge", play.Forge, "pr", moment.Play)
		return s.Store.MarkCardCommentChecked(ctx, id, now)
	}

	headline := moment.Headline(previous.Moment)
	commentID, found, err := findCardComment(ctx, fp, play)
	if err != nil {
		return err
	}
	if !found {
		if err := fp.Comment(ctx, play.Repo, play.Number, cardimage.CommentBody(card, now, headline, "")); err != nil {
			return err
		}
		if commentID, _, err = findCardComment(ctx, fp, play); err != nil {
			s.Log.Warn("card comment posted but not found again; it has no image this time", "work_item", id,
				"repo", play.Repo, "pr", play.Number, "err", err)
		}
	}
	imageURL := s.attachCard(ctx, fp, play, commentID, card, now)
	if found || imageURL != "" {
		if err := fp.EditComment(ctx, play.Repo, play.Number, commentID, cardimage.CommentBody(card, now, headline, imageURL)); err != nil {
			return err
		}
	}
	rec := store.CardComment{WorkItemID: id, PullRequestID: &play.ID, Moment: moment.Key, Image: imageURL != "", PublishedAt: &now, CheckedAt: now}
	if commentID > 0 {
		rec.CommentID = &commentID
	}
	if err := s.Store.RecordCardComment(ctx, rec); err != nil {
		return err
	}
	s.Log.Info("card comment published", "work_item", id, "moment", moment.Key, "repo", play.Repo, "pr", play.Number,
		"comment", commentID, "image", imageURL != "")
	return nil
}

func (s *Server) attachCard(ctx context.Context, fp provider.ForgeProvider, play store.CardCommentPlay, commentID int64, card store.OperatorCard, now time.Time) string {
	attacher, ok := fp.(provider.CommentAttacher)
	if !ok || commentID <= 0 {
		return ""
	}
	svg := cardimage.Render(card, cardimage.Options{Now: now})
	url, err := attacher.AttachToComment(ctx, play.Repo, play.Number, commentID,
		provider.Attachment{Name: cardimage.FileName(card), ContentType: cardimage.ContentType, Data: svg})
	if err != nil {
		s.Log.Warn("card image not attached; the comment shows the summary table", "work_item", card.WorkItemID,
			"repo", play.Repo, "pr", play.Number, "comment", commentID, "err", err)
		return ""
	}
	return url
}

var errNoCommentList = errors.New("comments not listed; the card comment is not posted blind")

func findCardComment(ctx context.Context, fp provider.ForgeProvider, play store.CardCommentPlay) (int64, bool, error) {
	comments, err := fp.Comments(ctx, play.Repo, play.Number)
	if err != nil {
		return 0, false, errors.Join(errNoCommentList, err)
	}
	for _, c := range comments {
		if cardimage.IsCardComment(c.Body) {
			return c.ID, true, nil
		}
	}
	return 0, false, nil
}
