package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

type publicationRefusal struct {
	status  int
	code    string
	message string
}

func (r *publicationRefusal) write(w http.ResponseWriter) {
	operatorError(w, r.status, r.code, r.message)
}

func (s *Server) logger() *slog.Logger {
	if s.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return s.Log
}

type registeredRepository struct {
	url      string
	path     string
	fullName string
}

func parseRegisteredRepository(raw string) (registeredRepository, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return registeredRepository{}, false
	}
	path := strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), ".git")
	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) < 2 || segments[len(segments)-2] == "" || segments[len(segments)-1] == "" {
		return registeredRepository{}, false
	}
	return registeredRepository{url: strings.TrimSuffix(raw, ".git"), path: path,
		fullName: segments[len(segments)-2] + "/" + segments[len(segments)-1]}, true
}

func (r registeredRepository) pullRequestNumber(remote *url.URL) (int, bool) {
	rest, found := strings.CutPrefix(remote.Path, r.path+"/pulls/")
	if !found {
		return 0, false
	}
	number, err := strconv.Atoi(rest)
	if err != nil || number <= 0 || strconv.Itoa(number) != rest {
		return 0, false
	}
	return number, true
}

func (s *Server) publishedPullRequestReader(repository registeredRepository) provider.PublishedPullRequestReader {
	names := make([]string, 0, len(s.Forges))
	for name := range s.Forges {
		names = append(names, name)
	}
	slices.Sort(names)
	var readers []provider.PublishedPullRequestReader
	for _, name := range names {
		reader, ok := s.Forges[name].(provider.PublishedPullRequestReader)
		if !ok {
			continue
		}
		if locator, ok := s.Forges[name].(provider.ForgeRepositoryLocator); ok {
			owner, repo, _ := strings.Cut(repository.fullName, "/")
			if expected, err := locator.RepositoryURL(owner, repo); err == nil && strings.TrimSuffix(expected, ".git") == repository.url {
				return reader
			}
		}
		readers = append(readers, reader)
	}
	if len(readers) == 1 {
		return readers[0]
	}
	return nil
}

func publicationMismatches(pr provider.PublishedPullRequest, op store.PublicationOperation, repository registeredRepository, policy DeliveryPolicy) []string {
	var mismatches []string
	if pr.HeadSHA != op.CanonicalSHA {
		mismatches = append(mismatches, "head commit")
	}
	if pr.HeadRef != strings.TrimPrefix(op.Branch, "refs/heads/") {
		mismatches = append(mismatches, "head branch")
	}
	if pr.BaseRef != op.BaseBranch {
		mismatches = append(mismatches, "base branch")
	}
	if !strings.EqualFold(pr.HeadRepository, repository.fullName) || !strings.EqualFold(pr.BaseRepository, repository.fullName) {
		mismatches = append(mismatches, "repository")
	}
	if pr.State != provider.PullRequestOpen && pr.State != provider.PullRequestMerged {
		mismatches = append(mismatches, "state")
	}
	if policy.PublisherLogin != "" && !strings.EqualFold(pr.Author, policy.PublisherLogin) {
		mismatches = append(mismatches, "author")
	}
	return mismatches
}

func (s *Server) verifyPublication(ctx context.Context, op store.PublicationOperation, policy DeliveryPolicy, remote *url.URL) *publicationRefusal {
	repository, ok := parseRegisteredRepository(op.RepositoryURL)
	if !ok {
		return &publicationRefusal{409, "publication_unverifiable", "The registered repository has no owner and name Ploeg can read."}
	}
	number, ok := repository.pullRequestNumber(remote)
	if !ok {
		return &publicationRefusal{400, "invalid_publication_evidence", "Positive publication evidence must name a pull request of the registered repository."}
	}
	reader := s.publishedPullRequestReader(repository)
	if reader == nil {
		return &publicationRefusal{409, "publication_unverifiable", "Ploeg has no forge reader for the registered repository, so it cannot confirm the pull request."}
	}
	pr, err := reader.PublishedPullRequest(ctx, repository.fullName, number)
	if err != nil {
		s.logger().Warn("publication check could not read the pull request", "repository", repository.fullName, "pull_request", number, "err", err)
		return &publicationRefusal{503, "forge_unavailable", "Ploeg could not read the pull request from the forge."}
	}
	if mismatches := publicationMismatches(pr, op, repository, policy); len(mismatches) > 0 {
		return &publicationRefusal{409, "publication_mismatch",
			fmt.Sprintf("The forge pull request does not match the reserved publication: %s.", strings.Join(mismatches, ", "))}
	}
	return nil
}

func (s *Server) notifyPublished(ctx context.Context, workItemID string, op store.PublicationOperation) {
	id, err := strconv.ParseInt(workItemID, 10, 64)
	if err != nil {
		return
	}
	item, err := s.Store.WorkItem(ctx, id)
	if err != nil {
		return
	}
	tp, ok := s.Trackers[item.Provider]
	if !ok {
		return
	}
	short := op.CanonicalSHA
	if len(short) > 12 {
		short = short[:12]
	}
	body := fmt.Sprintf("Ploeg confirmed the publication on the forge: %s (commit %s on %s).", op.RemoteURL, short, strings.TrimPrefix(op.Branch, "refs/heads/"))
	if err := tp.Comment(ctx, item.ExternalID, body); err != nil {
		s.logger().Error("tracker comment failed", "work_item", id, "err", err)
	}
}
