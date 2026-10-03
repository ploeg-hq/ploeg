package forgejo

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

var _ provider.CommentAttacher = (*Provider)(nil)

type assetForge struct {
	mu       sync.Mutex
	assets   []commentAsset
	deleted  []string
	uploads  int
	refuse   bool
	gotType  string
	gotName  string
	gotQuery string
	gotData  string
	gotAuth  string
}

func (f *assetForge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	const base = "/api/v1/repos/webgrip/ploeg/issues/comments/7/assets"
	switch {
	case r.Method == http.MethodGet && r.URL.Path == base:
		_ = json.NewEncoder(w).Encode(f.assets)
	case r.Method == http.MethodPost && r.URL.Path == base:
		f.uploads++
		f.gotAuth = r.Header.Get("Authorization")
		if f.refuse {
			http.Error(w, `{"message":"file type is not allowed"}`, http.StatusUnprocessableEntity)
			return
		}
		file, header, err := r.FormFile("attachment")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		data, _ := io.ReadAll(file)
		f.gotType, f.gotName, f.gotQuery, f.gotData = header.Header.Get("Content-Type"), header.Filename, r.URL.Query().Get("name"), string(data)
		a := commentAsset{ID: int64(100 + f.uploads), Name: header.Filename, DownloadURL: "https://forge.example/attachments/new-" + header.Filename}
		f.assets = append(f.assets, a)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(a)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, base+"/"):
		f.deleted = append(f.deleted, strings.TrimPrefix(r.URL.Path, base+"/"))
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func TestAttachToCommentUploadsAndReplacesTheEarlierImage(t *testing.T) {
	forge := &assetForge{assets: []commentAsset{{ID: 5, Name: "unfold-card-42.svg"}, {ID: 6, Name: "screenshot.png"}}}
	srv := httptest.NewServer(forge)
	defer srv.Close()
	p := &Provider{BaseURL: srv.URL, Token: "tok", Log: slog.New(slog.DiscardHandler)}

	got, err := p.AttachToComment(context.Background(), "webgrip/ploeg", 12, 7,
		provider.Attachment{Name: "unfold-card-42.svg", ContentType: "image/svg+xml", Data: []byte("<svg/>")})
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://forge.example/attachments/new-unfold-card-42.svg" {
		t.Errorf("url = %q", got)
	}
	if forge.gotType != "image/svg+xml" || forge.gotName != "unfold-card-42.svg" || forge.gotQuery != "unfold-card-42.svg" || forge.gotData != "<svg/>" {
		t.Errorf("upload = type %q name %q query %q data %q", forge.gotType, forge.gotName, forge.gotQuery, forge.gotData)
	}
	if forge.gotAuth != "token tok" {
		t.Errorf("authorization = %q", forge.gotAuth)
	}
	if len(forge.deleted) != 1 || forge.deleted[0] != "5" {
		t.Errorf("deleted = %v; want only the earlier card image 5", forge.deleted)
	}
}

func TestAttachToCommentReportsARefusedFileAndKeepsTheEarlierImage(t *testing.T) {
	forge := &assetForge{refuse: true, assets: []commentAsset{{ID: 5, Name: "unfold-card-42.svg"}}}
	srv := httptest.NewServer(forge)
	defer srv.Close()
	p := &Provider{BaseURL: srv.URL, Log: slog.New(slog.DiscardHandler)}

	_, err := p.AttachToComment(context.Background(), "webgrip/ploeg", 12, 7,
		provider.Attachment{Name: "unfold-card-42.svg", ContentType: "image/svg+xml", Data: []byte("<svg/>")})
	if err == nil || !strings.Contains(err.Error(), "HTTP 422") || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("err = %v; want the forge's refusal", err)
	}
	if len(forge.deleted) != 0 {
		t.Errorf("deleted %v after a failed upload", forge.deleted)
	}
}

func TestAttachToCommentRejectsBadInput(t *testing.T) {
	p := &Provider{BaseURL: "http://unused"}
	for name, call := range map[string]func() error{
		"repo": func() error {
			_, err := p.AttachToComment(context.Background(), "nope", 1, 1, provider.Attachment{Name: "a", Data: []byte("x")})
			return err
		},
		"comment": func() error {
			_, err := p.AttachToComment(context.Background(), "a/b", 1, 0, provider.Attachment{Name: "a", Data: []byte("x")})
			return err
		},
		"empty": func() error {
			_, err := p.AttachToComment(context.Background(), "a/b", 1, 1, provider.Attachment{Name: "a"})
			return err
		},
	} {
		if call() == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
