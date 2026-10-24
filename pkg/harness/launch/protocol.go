// Package launch carries a Run's processes from ploeg-worker to the harness's
// own container (ADR-0138). The worker listens on a Unix socket in a volume
// both containers mount, accepts exactly one connection from
// ploeg-worker harness-serve, removes the socket and multiplexes every
// process of the Run over that connection. No credential crosses it: the
// worker sends the environment it prepared for the harness, which carries
// placeholders and proxy endpoints only.
package launch

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ProtocolVersion is the version of the frames both sides speak.
const ProtocolVersion = 1

type frameKind byte

const (
	frameHello frameKind = iota + 1
	frameStart
	frameStarted
	frameStdin
	frameStdinClose
	frameStdout
	frameStdoutEnd
	frameStderr
	frameStderrEnd
	frameSignal
	frameExit
	frameQuery
	frameReply
)

const (
	headerSize   = 9
	maxFrameSize = 1 << 20
	chunkSize    = 32 << 10
)

type frame struct {
	kind    frameKind
	stream  uint32
	payload []byte
}

var errFrameTooLarge = errors.New("launcher frame exceeds the protocol's size limit")

func writeFrame(w io.Writer, f frame) error {
	if len(f.payload) > maxFrameSize {
		return errFrameTooLarge
	}
	buf := make([]byte, headerSize+len(f.payload))
	buf[0] = byte(f.kind)
	binary.BigEndian.PutUint32(buf[1:5], f.stream)
	binary.BigEndian.PutUint32(buf[5:9], uint32(len(f.payload)))
	copy(buf[headerSize:], f.payload)
	_, err := w.Write(buf)
	return err
}

func readFrame(r io.Reader) (frame, error) {
	var header [headerSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return frame{}, err
	}
	size := binary.BigEndian.Uint32(header[5:9])
	if size > maxFrameSize {
		return frame{}, errFrameTooLarge
	}
	f := frame{kind: frameKind(header[0]), stream: binary.BigEndian.Uint32(header[1:5]), payload: make([]byte, size)}
	if _, err := io.ReadFull(r, f.payload); err != nil {
		return frame{}, fmt.Errorf("read launcher frame: %w", err)
	}
	return f, nil
}

func jsonFrame(kind frameKind, stream uint32, v any) frame {
	payload, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return frame{kind: kind, stream: stream, payload: payload}
}

type hello struct {
	Protocol   int    `json:"protocol"`
	PID        int    `json:"pid"`
	Executable string `json:"executable,omitempty"`
}

type startRequest struct {
	Argv  []string `json:"argv"`
	Dir   string   `json:"dir,omitempty"`
	Env   []string `json:"env"`
	Stdin bool     `json:"stdin,omitempty"`
}

type started struct {
	Error string `json:"error,omitempty"`
}

type signalRequest struct {
	Signal int `json:"signal"`
}

type exited struct {
	Code  int    `json:"code"`
	Error string `json:"error,omitempty"`
}

const (
	queryLookPath = "lookpath"
	queryCheckDir = "checkdir"
	queryEnviron  = "environ"
	queryKillAll  = "killall"
)

type query struct {
	Op   string `json:"op"`
	File string `json:"file,omitempty"`
	Path string `json:"path,omitempty"`
	Dir  string `json:"dir,omitempty"`
}

type reply struct {
	Path  string   `json:"path,omitempty"`
	Env   []string `json:"env,omitempty"`
	Error string   `json:"error,omitempty"`
}
