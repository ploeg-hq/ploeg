package worker

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/ploeg-hq/ploeg/pkg/litellm"
)

const refusalBodyWindow = 64 << 10

type modelTraffic struct {
	isolated         bool
	modelCalls       atomic.Int64
	keyBudgetRefused atomic.Bool
}

func (t *modelTraffic) observe(resp *http.Response) {
	if resp.StatusCode < 400 || resp.Body == nil {
		return
	}
	head, err := io.ReadAll(io.LimitReader(resp.Body, refusalBodyWindow))
	resp.Body = readCloser{Reader: io.MultiReader(bytes.NewReader(head), resp.Body), Closer: resp.Body}
	if err != nil {
		return
	}
	gw := litellm.ClassifyError(resp.StatusCode, head)
	if gw.Kind == litellm.ErrorBudgetExceeded && gw.BudgetOwner == litellm.BudgetOfKey {
		t.keyBudgetRefused.Store(true)
	}
}

type readCloser struct {
	io.Reader
	io.Closer
}

var modelCallSuffixes = []string{"/chat/completions", "/completions", "/responses", "/messages", ":generateContent", ":streamGenerateContent"}

func isModelCall(path string) bool {
	path = strings.TrimRight(path, "/")
	for _, suffix := range modelCallSuffixes {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	return false
}
