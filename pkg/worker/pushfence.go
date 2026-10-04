package worker

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const (
	flushPkt            = "0000"
	pktLengthSize       = 4
	maxPktLength        = 65520
	maxCommandListBytes = 1 << 20
	pushCertificate     = "push-cert"
	shallowAdvertised   = "shallow "
)

var errUnparseablePush = errors.New("the push's ref update list is not a well-formed pkt-line command list")

type pushFence struct {
	runBranchRef string
}

func newPushFence(runBranch string) pushFence {
	return pushFence{runBranchRef: "refs/heads/" + runBranch}
}

func isReceivePack(r *http.Request) bool {
	return r.Method == http.MethodPost && strings.HasSuffix(strings.ToLower(r.URL.Path), "/git-receive-pack")
}

func (f pushFence) admit(r *http.Request) error {
	body, err := decodedPushBody(r)
	if err != nil {
		return err
	}
	var inspected bytes.Buffer
	updates, err := readRefUpdates(io.LimitReader(io.TeeReader(body, &inspected), maxCommandListBytes))
	if err != nil {
		return err
	}
	for _, u := range updates {
		if err := f.permits(u); err != nil {
			return err
		}
	}
	replay := io.MultiReader(&inspected, body)
	r.Body = struct {
		io.Reader
		io.Closer
	}{replay, r.Body}
	return nil
}

func decodedPushBody(r *http.Request) (io.Reader, error) {
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))) {
	case "", "identity":
		return r.Body, nil
	case "gzip", "x-gzip":
		inflated, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, errUnparseablePush
		}
		r.Header.Del("Content-Encoding")
		r.ContentLength = -1
		r.Header.Del("Content-Length")
		return inflated, nil
	default:
		return nil, fmt.Errorf("the push body's Content-Encoding %q cannot be inspected", r.Header.Get("Content-Encoding"))
	}
}

type refUpdate struct {
	oldID, newID, ref string
}

func (u refUpdate) deletes() bool {
	return strings.Trim(u.newID, "0") == ""
}

func (f pushFence) permits(u refUpdate) error {
	if u.ref != f.runBranchRef {
		return fmt.Errorf("this Run may push only %s, not %s", f.runBranchRef, u.ref)
	}
	if u.deletes() {
		return fmt.Errorf("this Run may not delete its own branch %s", u.ref)
	}
	return nil
}

func readRefUpdates(r io.Reader) ([]refUpdate, error) {
	var updates []refUpdate
	for {
		payload, flush, err := readPktLine(r)
		if err != nil {
			return nil, err
		}
		if flush {
			return updates, nil
		}
		line := strings.TrimSuffix(payload, "\n")
		if len(updates) == 0 && strings.HasPrefix(line, shallowAdvertised) {
			continue
		}
		if len(updates) == 0 {
			if command, _, hasCapabilities := strings.Cut(line, "\x00"); hasCapabilities {
				line = command
			}
		}
		u, err := parseRefUpdate(line)
		if err != nil {
			return nil, err
		}
		updates = append(updates, u)
	}
}

func readPktLine(r io.Reader) (payload string, flush bool, err error) {
	var size [pktLengthSize]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return "", false, errUnparseablePush
	}
	if string(size[:]) == flushPkt {
		return "", true, nil
	}
	length, err := strconv.ParseUint(string(size[:]), 16, 32)
	if err != nil || length <= pktLengthSize || length > maxPktLength {
		return "", false, errUnparseablePush
	}
	data := make([]byte, length-pktLengthSize)
	if _, err := io.ReadFull(r, data); err != nil {
		return "", false, errUnparseablePush
	}
	return string(data), false, nil
}

func parseRefUpdate(line string) (refUpdate, error) {
	fields := strings.Split(line, " ")
	if len(fields) != 3 || fields[0] == pushCertificate {
		return refUpdate{}, errUnparseablePush
	}
	u := refUpdate{oldID: fields[0], newID: fields[1], ref: fields[2]}
	if !isObjectID(u.oldID) || !isObjectID(u.newID) || len(u.oldID) != len(u.newID) || !strings.HasPrefix(u.ref, "refs/") {
		return refUpdate{}, errUnparseablePush
	}
	return u, nil
}

func isObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}
