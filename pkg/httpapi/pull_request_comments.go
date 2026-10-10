package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/svgsafe"
)

// Bounds of a keyed pull request comment (ADR-0079).
const (
	PullRequestCommentMaxMarkdown = 32768
	pullRequestCommentMaxBody     = 384 * 1024
	pullRequestCommentMaxAlt      = 256
	pullRequestCommentTimeout     = 30 * time.Second
)

var pullRequestCommentKey = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// PullRequestCommentMarker is the hidden line that identifies the comment
// kept under key on a pull request.
func PullRequestCommentMarker(key string) string { return "<!-- ploeg:comment:" + key + " -->" }

const ploegMarkerPrefix = "<!-- ploeg:"

type pullRequestCommentRequest struct {
	Markdown string `json:"markdown"`
	Image    *struct {
		SVG string `json:"svg"`
		Alt string `json:"alt"`
	} `json:"image"`
	Number         int   `json:"number"`
	AdoptCommentID int64 `json:"adoptCommentId"`
}

type pullRequestCommentView struct {
	Key         string               `json:"key"`
	WorkItemID  string               `json:"workItemId"`
	PullRequest store.PullRequestRef `json:"pullRequest"`
	CommentID   *int64               `json:"commentId"`
	ImageURL    *string              `json:"imageUrl"`
	Created     bool                 `json:"created"`
	UpdatedAt   time.Time            `json:"updatedAt"`
}

func commentActor(w http.ResponseWriter, r *http.Request) (OperatorPrincipal, string, bool) {
	p, actor, ok := executionActor(w, r)
	if !ok {
		return p, "", false
	}
	if acting := r.Header.Get("X-Ploeg-Acting-User"); acting != "" {
		actor = acting
	}
	return p, "operator:" + p.Name + ":" + actor, true
}

func commentTarget(w http.ResponseWriter, r *http.Request) (int64, string, bool) {
	id, ok := operatorID(w, r)
	if !ok {
		return 0, "", false
	}
	key := r.PathValue("key")
	if !pullRequestCommentKey.MatchString(key) {
		operatorError(w, 400, "invalid_key", "A comment key is 1 to 63 lowercase letters, digits and dashes.")
		return 0, "", false
	}
	return id, key, true
}

func decodeCommentRequest(w http.ResponseWriter, r *http.Request) (pullRequestCommentRequest, bool) {
	var req pullRequestCommentRequest
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		operatorError(w, 415, "json_required", "Use application/json.")
		return req, false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, pullRequestCommentMaxBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			operatorError(w, 413, "too_large", "The comment request exceeds 384 KiB.")
			return req, false
		}
		operatorError(w, 400, "invalid_request", "The comment request is invalid.")
		return req, false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		operatorError(w, 400, "invalid_request", "Supply one JSON object.")
		return req, false
	}
	n := utf8.RuneCountInString(req.Markdown)
	switch {
	case !utf8.ValidString(req.Markdown) || n < 1 || n > PullRequestCommentMaxMarkdown:
		operatorError(w, 400, "invalid_request", "markdown must be 1 to 32768 characters of UTF-8.")
		return req, false
	case strings.Contains(req.Markdown, ploegMarkerPrefix):
		operatorError(w, 400, "invalid_request", "markdown must not carry a Ploeg marker.")
		return req, false
	case req.Number < 0 || req.AdoptCommentID < 0:
		operatorError(w, 400, "invalid_request", "number and adoptCommentId must be positive.")
		return req, false
	}
	if req.Image != nil {
		if utf8.RuneCountInString(req.Image.Alt) > pullRequestCommentMaxAlt {
			operatorError(w, 400, "invalid_request", "image alt must be at most 256 characters.")
			return req, false
		}
		if err := svgsafe.Check([]byte(req.Image.SVG)); err != nil {
			operatorError(w, 422, "unsafe_svg", err.Error())
			return req, false
		}
	}
	return req, true
}

func commentBody(key, markdown, alt, imageURL string) string {
	var b strings.Builder
	b.WriteString(PullRequestCommentMarker(key))
	b.WriteString("\n")
	if imageURL != "" {
		if alt == "" {
			alt = "image"
		}
		alt = strings.NewReplacer("[", "", "]", "", "\n", " ", "\r", " ").Replace(alt)
		b.WriteString("![" + alt + "](" + imageURL + ")\n\n")
	}
	b.WriteString(markdown)
	return b.String()
}

func findKeyedComment(comments []provider.Comment, key string) (int64, bool) {
	marker := PullRequestCommentMarker(key)
	for _, c := range comments {
		if strings.Contains(c.Body, marker) {
			return c.ID, true
		}
	}
	return 0, false
}

func commentRefError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNoPullRequest):
		operatorError(w, 409, "no_pull_request", "The Work Item has no recorded pull request.")
	default:
		operatorReadError(w, err)
	}
}

func (s *Server) handlePutPullRequestComment(w http.ResponseWriter, r *http.Request) {
	principal, actor, ok := commentActor(w, r)
	if !ok {
		return
	}
	id, key, ok := commentTarget(w, r)
	if !ok {
		return
	}
	req, ok := decodeCommentRequest(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), pullRequestCommentTimeout)
	defer cancel()
	ref, err := s.Store.CommentPullRequest(ctx, id, principal.Teams, req.Number)
	if err != nil {
		commentRefError(w, err)
		return
	}
	fp := s.Forges[ref.Forge]
	if fp == nil {
		operatorError(w, 503, "forge_unavailable", "Ploeg has no provider for the pull request's forge.")
		return
	}
	unlock, held, err := s.Store.LockPullRequestComment(ctx, ref.RowID(), key)
	if err != nil {
		operatorReadError(w, err)
		return
	}
	if !held {
		operatorError(w, 409, "comment_busy", "Another request is writing this comment; retry.")
		return
	}
	defer unlock()

	comments, err := fp.Comments(ctx, ref.FullName(), ref.Number)
	if err != nil {
		s.Log.Warn("keyed comment not written: comments not listed", "work_item", id, "key", key, "err", err)
		operatorError(w, 502, "forge_unavailable", "The forge did not list the pull request's comments.")
		return
	}
	commentID, found := findKeyedComment(comments, key)
	if !found && req.AdoptCommentID > 0 {
		for _, c := range comments {
			if c.ID == req.AdoptCommentID && strings.Contains(c.Body, ploegMarkerPrefix) {
				commentID, found = c.ID, true
			}
		}
		if !found {
			operatorError(w, 422, "adopt_not_found", "adoptCommentId names no Ploeg-marked comment on the pull request.")
			return
		}
	}
	alt := ""
	if req.Image != nil {
		alt = req.Image.Alt
	}
	created := false
	if !found {
		if err := fp.Comment(ctx, ref.FullName(), ref.Number, commentBody(key, req.Markdown, "", "")); err != nil {
			s.Log.Warn("keyed comment not posted", "work_item", id, "key", key, "err", err)
			operatorError(w, 502, "forge_unavailable", "The forge refused the comment.")
			return
		}
		created = true
		if again, err := fp.Comments(ctx, ref.FullName(), ref.Number); err == nil {
			commentID, _ = findKeyedComment(again, key)
		} else {
			s.Log.Warn("keyed comment posted but not listed again", "work_item", id, "key", key, "err", err)
		}
	}
	imageURL := ""
	if req.Image != nil && commentID > 0 {
		if attacher, ok := fp.(provider.CommentAttacher); ok {
			imageURL, err = attacher.AttachToComment(ctx, ref.FullName(), ref.Number, commentID,
				provider.Attachment{Name: "comment-" + key + ".svg", ContentType: "image/svg+xml", Data: []byte(req.Image.SVG)})
			if err != nil {
				s.Log.Warn("keyed comment image not attached; the comment carries the markdown alone", "work_item", id, "key", key, "err", err)
				imageURL = ""
			}
		}
	}
	if found || imageURL != "" {
		if err := fp.EditComment(ctx, ref.FullName(), ref.Number, commentID, commentBody(key, req.Markdown, alt, imageURL)); err != nil {
			s.Log.Warn("keyed comment not edited", "work_item", id, "key", key, "comment", commentID, "err", err)
			operatorError(w, 502, "forge_unavailable", "The forge refused the comment edit.")
			return
		}
	}
	now := time.Now().UTC()
	rec := store.PullRequestComment{PullRequest: ref, Key: key, Actor: actor, UpdatedAt: now}
	if commentID > 0 {
		rec.CommentID = &commentID
	}
	if imageURL != "" {
		rec.ImageURL = &imageURL
	}
	if err := s.Store.RecordPullRequestComment(ctx, id, rec, created); err != nil {
		operatorError(w, 503, "unavailable", "Ploeg wrote the comment but could not record it; repeat the request.")
		return
	}
	s.Log.Info("keyed comment written", "work_item", id, "key", key, "repo", ref.FullName(), "pr", ref.Number,
		"comment", commentID, "created", created, "image", imageURL != "")
	operatorJSON(w, http.StatusOK, map[string]any{"schemaVersion": "1.0", "comment": pullRequestCommentView{
		Key: key, WorkItemID: strconv.FormatInt(id, 10), PullRequest: ref, CommentID: rec.CommentID, ImageURL: rec.ImageURL,
		Created: created, UpdatedAt: now}})
}

func (s *Server) handleDeletePullRequestComment(w http.ResponseWriter, r *http.Request) {
	principal, actor, ok := commentActor(w, r)
	if !ok {
		return
	}
	id, key, ok := commentTarget(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), pullRequestCommentTimeout)
	defer cancel()
	if _, err := s.Store.CommentPullRequest(ctx, id, principal.Teams, 0); err != nil && !errors.Is(err, store.ErrNoPullRequest) {
		commentRefError(w, err)
		return
	}
	recorded, err := s.Store.PullRequestComments(ctx, id, key)
	if err != nil {
		operatorReadError(w, err)
		return
	}
	deleted := []store.PullRequestRef{}
	for _, c := range recorded {
		fp := s.Forges[c.PullRequest.Forge]
		deleter, ok := fp.(provider.CommentDeleter)
		if fp == nil || !ok {
			operatorError(w, 503, "forge_unavailable", "Ploeg cannot delete comments on the pull request's forge.")
			return
		}
		unlock, held, err := s.Store.LockPullRequestComment(ctx, c.PullRequest.RowID(), key)
		if err != nil {
			operatorReadError(w, err)
			return
		}
		if !held {
			operatorError(w, 409, "comment_busy", "Another request is writing this comment; retry.")
			return
		}
		err = s.deleteKeyedComment(ctx, fp, deleter, c)
		if err == nil {
			err = s.Store.DeletePullRequestComment(ctx, id, c, actor)
		}
		unlock()
		if err != nil {
			s.Log.Warn("keyed comment not deleted", "work_item", id, "key", key, "repo", c.PullRequest.FullName(), "err", err)
			operatorError(w, 502, "forge_unavailable", "The forge did not delete the comment.")
			return
		}
		deleted = append(deleted, c.PullRequest)
	}
	operatorJSON(w, http.StatusOK, map[string]any{"schemaVersion": "1.0", "key": key, "workItemId": strconv.FormatInt(id, 10), "deleted": deleted})
}

func (s *Server) deleteKeyedComment(ctx context.Context, fp provider.ForgeProvider, deleter provider.CommentDeleter, c store.PullRequestComment) error {
	comments, err := fp.Comments(ctx, c.PullRequest.FullName(), c.PullRequest.Number)
	if err != nil {
		return err
	}
	commentID, found := findKeyedComment(comments, c.Key)
	if !found {
		return nil
	}
	return deleter.DeleteComment(ctx, c.PullRequest.FullName(), c.PullRequest.Number, commentID)
}
