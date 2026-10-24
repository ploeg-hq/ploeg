package worker

import (
	"bytes"
	"context"
	"os"
	"sync"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

type harnessSide struct {
	spawner harness.Spawner
	environ []string
}

func localHarnessSide() harnessSide {
	return harnessSide{spawner: harness.NewLocalSpawner(), environ: os.Environ()}
}

func (s harnessSide) path() string {
	path, _ := lookupEnv(s.environ, "PATH")
	return path
}

func (s harnessSide) lookPath(ctx context.Context, file string) (string, error) {
	return s.spawner.LookPath(ctx, file, s.path())
}

func (w *Worker) side() harnessSide {
	if w.Spawner == nil {
		w.Spawner = harness.NewLocalSpawner()
	}
	if w.HarnessEnviron == nil {
		return harnessSide{spawner: w.Spawner, environ: os.Environ()}
	}
	return harnessSide{spawner: w.Spawner, environ: w.HarnessEnviron}
}

// LookPathIn returns a lookPath that resolves a program on the PATH of
// environ through spawner, for CheckHarnessBinary and HarnessVersion.
func LookPathIn(ctx context.Context, spawner harness.Spawner, environ []string) func(string) (string, error) {
	side := harnessSide{spawner: spawner, environ: environ}
	return func(file string) (string, error) { return side.lookPath(ctx, file) }
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

func (b *lockedBuffer) String() string { return string(b.Bytes()) }
