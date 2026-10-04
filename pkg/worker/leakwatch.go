package worker

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type credentialKind string

const (
	credentialModelKey   credentialKind = "model key"
	credentialForgeToken credentialKind = "forge token"
	credentialCanary     credentialKind = "canary credential"
)

const (
	canaryEnv                  = "GITHUB_PAT"
	canaryPrefix               = "ghp_"
	canaryBodyLength           = 36
	canaryAlphabet             = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	minimumWatchedLength       = 12
	publishedBranchForLeakScan = "refs/ploeg/leak-scan"
	leakScanTimeout            = 5 * time.Minute
)

type credentialLeak struct {
	kind  credentialKind
	where string
}

type leakWatch struct {
	mu      sync.Mutex
	watched map[credentialKind][]string
	found   *credentialLeak
}

func newLeakWatch() *leakWatch {
	return &leakWatch{watched: map[credentialKind][]string{}}
}

func (w *leakWatch) guard(kind credentialKind, value string) {
	if w == nil || len(value) < minimumWatchedLength {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !slices.Contains(w.watched[kind], value) {
		w.watched[kind] = append(w.watched[kind], value)
	}
}

func (w *leakWatch) find(data []byte) (credentialKind, bool) {
	if w == nil {
		return "", false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, kind := range []credentialKind{credentialModelKey, credentialForgeToken, credentialCanary} {
		for _, value := range w.watched[kind] {
			if bytes.Contains(data, []byte(value)) {
				return kind, true
			}
		}
	}
	return "", false
}

func (w *leakWatch) longestWatched() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	longest := 0
	for _, values := range w.watched {
		for _, v := range values {
			longest = max(longest, len(v))
		}
	}
	return longest
}

func (w *leakWatch) findInStream(r io.Reader) (credentialKind, bool, error) {
	if w == nil {
		return "", false, nil
	}
	overlap := max(w.longestWatched()-1, 0)
	window := make([]byte, 0, 64<<10+overlap)
	chunk := make([]byte, 64<<10)
	for {
		n, err := r.Read(chunk)
		window = append(window, chunk[:n]...)
		if kind, ok := w.find(window); ok {
			return kind, true, nil
		}
		if len(window) > overlap {
			window = append(window[:0], window[len(window)-overlap:]...)
		}
		if err == io.EOF {
			return "", false, nil
		}
		if err != nil {
			return "", false, err
		}
	}
}

func (w *leakWatch) record(leak credentialLeak) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.found == nil {
		w.found = &leak
	}
}

func (w *leakWatch) leak() (credentialLeak, bool) {
	if w == nil {
		return credentialLeak{}, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.found == nil {
		return credentialLeak{}, false
	}
	return *w.found, true
}

func newCanaryCredential() (string, error) {
	var b strings.Builder
	b.WriteString(canaryPrefix)
	limit := big.NewInt(int64(len(canaryAlphabet)))
	for range canaryBodyLength {
		i, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("generate the Run's canary credential: %w", err)
		}
		b.WriteByte(canaryAlphabet[i.Int64()])
	}
	return b.String(), nil
}

type leakScope struct {
	run      string
	workItem string
	log      *slog.Logger
	watch    *leakWatch
}

func (s leakScope) report(leak credentialLeak) {
	s.watch.record(leak)
	s.log.Error("credential leak: the Run's output carried one of its credentials",
		"run", s.run, "work_item", s.workItem, "credential", string(leak.kind), "route", leak.where)
}

type pushedCommitScan struct {
	dir             string
	cloneURL        string
	token           string
	branch          string
	start           string
	publishedBefore branchHead
}

func scanPushedCommits(ctx context.Context, scope leakScope, c pushedCommitScan) error {
	repo, err := openWorkerRepository(ctx, c.dir, c.cloneURL, c.token)
	if err != nil {
		return err
	}
	defer repo.remove()
	head := repo.branchHead(ctx, c.branch)
	if head.err != nil {
		return head.err
	}
	if head.commit == "" || head.commit == c.publishedBefore.commit {
		return nil
	}
	if err := repo.fetchBranch(ctx, c.branch, publishedBranchForLeakScan); err != nil {
		return err
	}
	exclude := []string{c.start}
	if c.publishedBefore.commit != "" && repo.commitIsLocal(ctx, c.publishedBefore.commit) {
		exclude = append(exclude, c.publishedBefore.commit)
	}
	cmd := repo.local(ctx, "", slices.Concat([]string{
		"log", "--patch", "--text", "--no-color", "--no-ext-diff", "--no-textconv",
		"--format=%H%n%an <%ae>%n%cn <%ce>%n%B", publishedBranchForLeakScan, "--not",
	}, exclude)...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	kind, found, scanErr := scope.watch.findInStream(out)
	if found {
		_ = cmd.Process.Kill()
	}
	_, _ = io.Copy(io.Discard, out)
	waitErr := cmd.Wait()
	if found {
		scope.report(credentialLeak{kind: kind, where: "commits pushed to " + c.branch})
		return nil
	}
	if scanErr != nil {
		return scanErr
	}
	if waitErr != nil {
		return fmt.Errorf("git log %s: %v: %s", c.branch, waitErr, strings.TrimSpace(tail(stderr.Bytes(), 400)))
	}
	return nil
}

func credentialLeakReport(report harness.OutcomeReport, leak credentialLeak, branch string) harness.OutcomeReport {
	report.Outcome = work.OutcomeFailed
	report.FailureReason = string(work.FailureCredentialLeak)
	report.Summary = fmt.Sprintf("Ploeg stopped this Run: its %s appeared in %s. The commits already pushed stay on branch %s for forensics; "+
		"no pull request work from this Run is accepted. Rotate the credential and read the pushed commits before retrying.", leak.kind, leak.where, branch)
	report.StuckReason = ""
	report.Links = nil
	report.Checkpoint = nil
	report.Verdict = ""
	report.Findings = ""
	report.CreatedWorkItems = nil
	report.Learnings = nil
	return report
}

func guardCredentialLeaks(ctx context.Context, report harness.OutcomeReport, scope leakScope, scan pushedCommitScan, writes bool) harness.OutcomeReport {
	if !writes {
		return report
	}
	scanErr := scanPushedCommits(ctx, scope, scan)
	if leak, leaked := scope.watch.leak(); leaked {
		return credentialLeakReport(report, leak, scan.branch)
	}
	if scanErr != nil && report.Outcome.AssertsDelivery() {
		report.Outcome = work.OutcomeStuck
		report.Summary = "Ploeg could not check the pushed commits for leaked credentials"
		report.StuckReason = "the credential leak scan of branch " + scan.branch + " failed, so the Run is not reported ready for review: " + scanErr.Error()
		report.Links = nil
		report.Verdict = ""
	}
	return report
}

func withholdLeakedLearnings(report harness.OutcomeReport, scope leakScope, branch string) harness.OutcomeReport {
	if len(report.Learnings) == 0 {
		return report
	}
	if _, leaked := scope.watch.leak(); leaked {
		report.Learnings = nil
		return report
	}
	if kind, found := scope.watch.find(learningsText(report.Learnings)); found {
		leak := credentialLeak{kind: kind, where: "the Run's proposed learnings"}
		scope.report(leak)
		return credentialLeakReport(report, leak, branch)
	}
	return report
}

func learningsText(learnings []harness.Learning) []byte {
	var text bytes.Buffer
	for _, l := range learnings {
		for _, field := range append([]string{l.Type, l.Title, l.Description, l.Resource, l.Body}, l.Tags...) {
			text.WriteString(field)
			text.WriteByte('\n')
		}
	}
	return text.Bytes()
}
