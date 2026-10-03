//go:build !unix

package acp

import (
	"context"
	"errors"
	"io"
	"os"
)

// Ploeg's workers run in Linux pods and development happens on macOS; both are
// unix. This stub exists only so the package still builds elsewhere, rather
// than the whole module failing to compile on an unsupported host.
type execLauncher struct{}

func (execLauncher) launch(context.Context, []string, string, []string, io.Writer, func([]byte)) (*process, error) {
	return nil, errors.New("acp: the subprocess launcher requires a unix host")
}

var syscallTERM os.Signal = os.Interrupt
