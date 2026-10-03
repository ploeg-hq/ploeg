package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

var errUnsupportedForge = errors.New("unsupported forge dialect")

type changeRequest struct {
	URL        string
	HeadBranch string
	BaseBranch string
}

func isRunChangeRequest(cr changeRequest, runBranch, requiredBase string) bool {
	if cr.HeadBranch != runBranch {
		return false
	}
	return requiredBase == "" || cr.BaseBranch == requiredBase
}

const (
	forgejoPullsPageSize = 50
	forgejoPullsMaxPages = 20
)

var errTooManyOpenPullRequests = errors.New("too many open pull requests to search")

func findOpenChangeRequest(ref harness.RepoRef, token, runBranch string) (string, error) {
	match := func(cr changeRequest) bool { return isRunChangeRequest(cr, runBranch, ref.BaseBranch) }
	switch ref.Dialect() {
	case harness.ForgeForgejo:
		return findForgejoPullRequest(ref, token, match)
	case harness.ForgeGitLab:
		open, err := listGitLabMergeRequests(ref, token, runBranch)
		if err != nil {
			return "", err
		}
		for _, cr := range open {
			if match(cr) {
				return cr.URL, nil
			}
		}
		return "", nil
	default:
		return "", fmt.Errorf("%w: %q", errUnsupportedForge, ref.Forge)
	}
}

func findForgejoPullRequest(ref harness.RepoRef, token string, match func(changeRequest) bool) (string, error) {
	seen := 0
	for page := 1; page <= forgejoPullsMaxPages; page++ {
		pulls, total, err := listForgejoPullRequestsPage(ref, token, page)
		if err != nil {
			return "", err
		}
		for _, cr := range pulls {
			if match(cr) {
				return cr.URL, nil
			}
		}
		seen += len(pulls)
		if len(pulls) == 0 || (total >= 0 && seen >= total) || (total < 0 && len(pulls) < forgejoPullsPageSize) {
			return "", nil
		}
	}
	return "", fmt.Errorf("%w: %s/%s has more than %d", errTooManyOpenPullRequests,
		ref.Owner, ref.Name, forgejoPullsPageSize*forgejoPullsMaxPages)
}

func listForgejoPullRequestsPage(ref harness.RepoRef, token string, page int) (pulls []changeRequest, total int, err error) {
	req, err := http.NewRequest(http.MethodGet,
		fmt.Sprintf("%s/api/v1/repos/%s/%s/pulls?state=open&limit=%d&page=%d",
			ref.ForgeURL, ref.Owner, ref.Name, forgejoPullsPageSize, page), nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "token "+token)

	var raw []struct {
		HTMLURL string `json:"html_url"`
		Head    struct {
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	header, err := getJSONWithHeader(req, &raw)
	if err != nil {
		return nil, 0, err
	}
	total = -1
	if n, convErr := strconv.Atoi(header.Get("X-Total-Count")); convErr == nil && n >= 0 {
		total = n
	}
	pulls = make([]changeRequest, 0, len(raw))
	for _, p := range raw {
		pulls = append(pulls, changeRequest{URL: p.HTMLURL, HeadBranch: p.Head.Ref, BaseBranch: p.Base.Ref})
	}
	return pulls, total, nil
}

func listGitLabMergeRequests(ref harness.RepoRef, token, sourceBranch string) ([]changeRequest, error) {
	req, err := http.NewRequest(http.MethodGet,
		fmt.Sprintf("%s/api/v4/projects/%s/merge_requests?state=opened&source_branch=%s&per_page=50",
			ref.ForgeURL, url.QueryEscape(ref.ProjectPath()), url.QueryEscape(sourceBranch)), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", token)

	var merges []struct {
		WebURL       string `json:"web_url"`
		SourceBranch string `json:"source_branch"`
		TargetBranch string `json:"target_branch"`
	}
	if err := getJSON(req, &merges); err != nil {
		return nil, err
	}
	open := make([]changeRequest, 0, len(merges))
	for _, m := range merges {
		open = append(open, changeRequest{URL: m.WebURL, HeadBranch: m.SourceBranch, BaseBranch: m.TargetBranch})
	}
	return open, nil
}

func getJSON(req *http.Request, into any) error {
	_, err := getJSONWithHeader(req, into)
	return err
}

func getJSONWithHeader(req *http.Request, into any) (http.Header, error) {
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s %s: HTTP %d", req.Method, req.URL.Path, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
		return nil, err
	}
	return resp.Header, nil
}
