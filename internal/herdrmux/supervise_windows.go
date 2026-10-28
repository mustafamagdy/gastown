//go:build windows

package herdrmux

import (
	"context"
	"fmt"
	"os"
)

// Supervise is not supported on Windows (herdr has no Windows build).
func Supervise(ctx context.Context, sock, session string) int {
	fmt.Fprintln(os.Stderr, "mux-supervise: not supported on windows")
	return 1
}
