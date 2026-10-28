package herdrmux

import (
	"context"
	"errors"
	"fmt"
	"io"
)

var errUnsupported = errors.New("herdr backend is not supported on Windows")

// Run reports that the herdr backend is unavailable on Windows.
func Run(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fmt.Fprintln(stderr, errUnsupported)
	return 1
}

// RunHook reports that the herdr backend is unavailable on Windows.
func RunHook(ctx context.Context, sock, session, hook string, stdout, stderr io.Writer) error {
	return errUnsupported
}
