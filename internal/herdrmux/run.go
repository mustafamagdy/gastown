package herdrmux

import (
	"context"
	"fmt"
	"io"
)

// Run executes a tmux command line (argv without the leading "tmux") against
// herdr and returns the process exit code tmux would have produced.
func Run(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fmt.Fprintln(stderr, "herdrmux: not implemented")
	return 1
}
