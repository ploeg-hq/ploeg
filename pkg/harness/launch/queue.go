package launch

import (
	"io"
	"sync"
)

type chunkQueue struct {
	mu     sync.Mutex
	ready  *sync.Cond
	chunks [][]byte
	head   []byte
	closed bool
	err    error
}

func newChunkQueue() *chunkQueue {
	q := &chunkQueue{}
	q.ready = sync.NewCond(&q.mu)
	return q
}

func (q *chunkQueue) push(b []byte) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.chunks = append(q.chunks, b)
	q.ready.Signal()
}

func (q *chunkQueue) close(err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.closed, q.err = true, err
	q.ready.Broadcast()
}

func (q *chunkQueue) pop() ([]byte, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.chunks) == 0 && !q.closed {
		q.ready.Wait()
	}
	if len(q.chunks) == 0 {
		return nil, false
	}
	b := q.chunks[0]
	q.chunks = q.chunks[1:]
	return b, true
}

func (q *chunkQueue) Read(p []byte) (int, error) {
	if len(q.head) == 0 {
		b, ok := q.pop()
		if !ok {
			q.mu.Lock()
			err := q.err
			q.mu.Unlock()
			if err == nil {
				err = io.EOF
			}
			return 0, err
		}
		q.head = b
	}
	n := copy(p, q.head)
	q.head = q.head[n:]
	return n, nil
}
