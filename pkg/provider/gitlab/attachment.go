package gitlab

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
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

// AttachToComment uploads file to the project (POST
// /projects/:id/uploads) and returns its project-relative URL, which a note
// on any of the project's merge requests embeds. GitLab keeps uploads per
// project, not per note, so commentID only identifies the caller's note and
// an earlier upload is left in place.
func (p *Provider) AttachToComment(ctx context.Context, repo string, mr int, commentID int64, file provider.Attachment) (string, error) {
	if err := validRepo(repo); err != nil {
		return "", err
	}
	if file.Name == "" || len(file.Data) == 0 {
		return "", errors.New("gitlab: an attachment needs a name and content")
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, file.Name))
	contentType := file.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	header.Set("Content-Type", contentType)
	part, err := w.CreatePart(header)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(file.Data); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	endpoint := fmt.Sprintf("%s/api/v4/projects/%s/uploads", strings.TrimRight(p.BaseURL, "/"), url.PathEscape(repo))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	if p.Token != "" {
		req.Header.Set("PRIVATE-TOKEN", p.Token)
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("gitlab: upload %s for note %d on %s!%d: HTTP %d: %s", file.Name, commentID, repo, mr, resp.StatusCode, bytes.TrimSpace(snippet))
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return "", fmt.Errorf("gitlab: upload %s for note %d on %s!%d: %w", file.Name, commentID, repo, mr, err)
	}
	if out.URL == "" {
		return "", fmt.Errorf("gitlab: upload %s for note %d on %s!%d: no URL in the response", file.Name, commentID, repo, mr)
	}
	return out.URL, nil
}
