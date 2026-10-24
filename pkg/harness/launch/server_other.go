//go:build !unix

package launch

import (
	"context"
	"errors"
	"net"
)

var errUnsupported = errors.New("launch: the harness launcher requires a unix host")

// Serve is unavailable off unix: Ploeg's workers run in Linux pods.
func Serve(context.Context, string) error { return errUnsupported }

// ServeConn is unavailable off unix.
func ServeConn(context.Context, net.Conn) error { return errUnsupported }
