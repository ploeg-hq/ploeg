package forgejo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

type commentAsset struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
}

// AttachToComment uploads file as an asset of issue comment commentID
// (POST /repos/{owner}/{repo}/issues/comments/{id}/assets) and returns its
// browser download URL. Once the upload succeeded, earlier assets of the
// comment with the same name are deleted, so an edited keyed comment keeps
// one image. A failed delete is logged, never returned.
func (p *Provider) AttachToComment(ctx context.Context, repo string, pr int, commentID int64, file provider.Attachment) (string, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return "", fmt.Errorf("forgejo: repo %q must be owner/name", repo)
	}
	if commentID <= 0 {
		return "", fmt.Errorf("forgejo: comment id must be positive, got %d", commentID)
	}
	if file.Name == "" || len(file.Data) == 0 {
		return "", errors.New("forgejo: an attachment needs a name and content")
	}
	base := fmt.Sprintf("%s/api/v1/repos/%s/%s/issues/comments/%d/assets",
		strings.TrimRight(p.BaseURL, "/"), url.PathEscape(owner), url.PathEscape(name), commentID)
	earlier, listErr := p.commentAssets(ctx, base)
	if listErr != nil {
		p.log().Warn("forgejo: comment assets not listed; earlier comment images stay", "repo", repo, "pr", pr, "comment", commentID, "err", listErr)
	}
	uploaded, err := p.uploadCommentAsset(ctx, base, file)
	if err != nil {
		return "", fmt.Errorf("forgejo: attach %s to comment %d on %s#%d: %w", file.Name, commentID, repo, pr, err)
	}
	for _, a := range earlier {
		if a.Name != file.Name || a.ID == uploaded.ID {
			continue
		}
		if err := p.deleteCommentAsset(ctx, base+"/"+strconv.FormatInt(a.ID, 10)); err != nil {
			p.log().Warn("forgejo: earlier comment image not deleted", "repo", repo, "pr", pr, "comment", commentID, "asset", a.ID, "err", err)
		}
	}
	if uploaded.DownloadURL == "" {
		return "", fmt.Errorf("forgejo: attach %s to comment %d on %s#%d: no download URL in the response", file.Name, commentID, repo, pr)
	}
	return uploaded.DownloadURL, nil
}

func (p *Provider) authorize(req *http.Request) {
	req.Header.Set("Accept", "application/json")
	if p.Token != "" {
		req.Header.Set("Authorization", "token "+p.Token)
	}
}

func (p *Provider) commentAssets(ctx context.Context, endpoint string) ([]commentAsset, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	p.authorize(req)
	resp, err := p.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(snippet))
	}
	var out []commentAsset
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func (p *Provider) uploadCommentAsset(ctx context.Context, endpoint string, file provider.Attachment) (commentAsset, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="attachment"; filename=%q`, file.Name))
	contentType := file.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	header.Set("Content-Type", contentType)
	part, err := w.CreatePart(header)
	if err != nil {
		return commentAsset{}, err
	}
	if _, err := part.Write(file.Data); err != nil {
		return commentAsset{}, err
	}
	if err := w.Close(); err != nil {
		return commentAsset{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"?name="+url.QueryEscape(file.Name), &body)
	if err != nil {
		return commentAsset{}, err
	}
	p.authorize(req)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := p.client().Do(req)
	if err != nil {
		return commentAsset{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return commentAsset{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(snippet))
	}
	var out commentAsset
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return commentAsset{}, err
	}
	return out, nil
}

func (p *Provider) deleteCommentAsset(ctx context.Context, endpoint string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	p.authorize(req)
	resp, err := p.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(snippet))
	}
	return nil
}
