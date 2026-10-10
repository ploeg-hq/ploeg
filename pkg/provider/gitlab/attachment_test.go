package gitlab

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

var _ provider.CommentAttacher = (*Provider)(nil)

func TestAttachToCommentUploadsToTheProject(t *testing.T) {
	var path, token, name, contentType, data string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, token = r.URL.EscapedPath(), r.Header.Get("PRIVATE-TOKEN")
		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		raw, _ := io.ReadAll(file)
		name, contentType, data = header.Filename, header.Header.Get("Content-Type"), string(raw)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":5,"url":"/uploads/abc/comment-42.svg","markdown":"![x](/uploads/abc/comment-42.svg)"}`))
	}))
	defer srv.Close()
	p := &Provider{BaseURL: srv.URL, Token: "tok"}

	got, err := p.AttachToComment(context.Background(), "group/sub/app", 3, 9,
		provider.Attachment{Name: "comment-42.svg", ContentType: "image/svg+xml", Data: []byte("<svg/>")})
	if err != nil {
		t.Fatal(err)
	}
	if got != "/uploads/abc/comment-42.svg" {
		t.Errorf("url = %q", got)
	}
	if path != "/api/v4/projects/group%2Fsub%2Fapp/uploads" || token != "tok" {
		t.Errorf("request = %s token %q", path, token)
	}
	if name != "comment-42.svg" || contentType != "image/svg+xml" || data != "<svg/>" {
		t.Errorf("file = %q %q %q", name, contentType, data)
	}
}

func TestAttachToCommentReportsARefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"413 Request Entity Too Large"}`, http.StatusRequestEntityTooLarge)
	}))
	defer srv.Close()
	p := &Provider{BaseURL: srv.URL}
	_, err := p.AttachToComment(context.Background(), "group/app", 3, 9, provider.Attachment{Name: "a.svg", Data: []byte("x")})
	if err == nil || !strings.Contains(err.Error(), "HTTP 413") {
		t.Fatalf("err = %v", err)
	}
}
