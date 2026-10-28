package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness/claudeplugins"
)

// RunIdentity is one Run identity ploegd signed for an audience (ADR-0149).
type RunIdentity struct {
	Audience  string    `json:"audience"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// ErrRunIdentityOff means ploegd issues no Run identities.
var ErrRunIdentityOff = errors.New("ploegd issues no run identities")

// ErrRunIdentityEnded means ploegd no longer issues a Run identity for this
// Run: it is not running any more, for example because it yielded to a
// person (ADR-0142).
var ErrRunIdentityEnded = errors.New("the Run is not running, so it has no run identity")

// ErrRunIdentityRefused means ploegd signs none of the requested audiences
// for this Run's Team and Role (threat model F-48). The worker asks with the
// same audiences every time, so asking again cannot succeed.
var ErrRunIdentityRefused = errors.New("ploegd signs no run identity for an audience this Run's Team and Role do not list")

// RunIdentities asks ploegd for one Run identity per audience. The facts in
// them are ploegd's; the worker names only the audiences.
func (a *APIClient) RunIdentities(ctx context.Context, runToken string, audiences []string) ([]RunIdentity, error) {
	body, err := json.Marshal(map[string]any{"audiences": audiences})
	if err != nil {
		return nil, err
	}
	resp, err := a.request(ctx, http.MethodPost, "/api/v1/runs/"+runToken+"/identity", body, a.runCredential(runToken))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, ErrRunIdentityOff
	case http.StatusConflict:
		return nil, ErrRunIdentityEnded
	case http.StatusForbidden:
		return nil, ErrRunIdentityRefused
	default:
		return nil, fmt.Errorf("run identity: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Identities []RunIdentity `json:"identities"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil {
		return nil, err
	}
	return out.Identities, nil
}

type runIdentitySource interface {
	RunIdentities(ctx context.Context, runToken string, audiences []string) ([]RunIdentity, error)
}

// runIdentityKeeper keeps each opted-in plugin's Run identity file current
// while the harness runs: it fetches the tokens before the harness starts,
// again every interval (a third of the lease, so each token outlives the
// next refresh), and removes the files when the Run's harness is done.
type runIdentityKeeper struct {
	files map[string][]string
	stop  context.CancelFunc
	done  chan struct{}
	once  sync.Once
}

func startRunIdentity(ctx context.Context, api runIdentitySource, runToken string, results []claudeplugins.Result, every time.Duration, log *slog.Logger) *runIdentityKeeper {
	files := map[string][]string{}
	var audiences []string
	for _, r := range results {
		if r.RunIdentityFile == "" {
			continue
		}
		if _, ok := files[r.Audience]; !ok {
			audiences = append(audiences, r.Audience)
		}
		files[r.Audience] = append(files[r.Audience], r.RunIdentityFile)
	}
	if len(audiences) == 0 || api == nil {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	k := &runIdentityKeeper{files: files, stop: cancel, done: make(chan struct{})}
	refresh := func() bool {
		callCtx, cancelCall := context.WithTimeout(ctx, 10*time.Second)
		defer cancelCall()
		ids, err := api.RunIdentities(callCtx, runToken, audiences)
		if errors.Is(err, ErrRunIdentityOff) {
			log.Warn("plugins opted into the run identity, but ploegd issues none; their hooks run without it")
			k.remove()
			return false
		}
		if errors.Is(err, ErrRunIdentityRefused) {
			log.Warn("ploegd refused the run identity: an audience a plugin opted into is not configured for this Run's Team and Role; the plugins' hooks run without it", "audiences", audiences)
			k.remove()
			return false
		}
		if errors.Is(err, ErrRunIdentityEnded) {
			log.Info("ploegd ended the Run's identity; the plugin's hooks run without it from now on")
			k.remove()
			return false
		}
		if err != nil {
			log.Warn("run identity not refreshed; hooks keep the last one until it expires", "err", err)
			return true
		}
		for _, id := range ids {
			for _, path := range files[id.Audience] {
				if err := writeFileAtomically(path, []byte(id.Token), 0o600); err != nil {
					log.Warn("run identity not written; the plugin's hooks run without it", "audience", id.Audience, "err", err)
				}
			}
		}
		return true
	}
	if !refresh() {
		close(k.done)
		return k
	}
	log.Info("run identity issued to the plugins that opted in", "audiences", len(audiences))
	go func() {
		defer close(k.done)
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if !refresh() {
					return
				}
			}
		}
	}()
	return k
}

// Close stops refreshing and removes every Run identity file, so nothing
// the harness leaves behind holds a token once the Run's agent is done.
func (k *runIdentityKeeper) Close() {
	if k == nil {
		return
	}
	k.once.Do(func() {
		k.stop()
		<-k.done
		k.remove()
	})
}

func (k *runIdentityKeeper) remove() {
	for _, paths := range k.files {
		for _, p := range paths {
			_ = os.Remove(p)
		}
	}
}

func writeFileAtomically(path string, body []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".run-identity-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}
