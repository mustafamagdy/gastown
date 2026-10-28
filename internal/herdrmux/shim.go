package herdrmux

import "errors"

// EnsureShim writes the `tmux` shim script that forwards to `gt mux-tmux` and
// returns the directory containing it (to be prepended to PATH).
func EnsureShim() (string, error) {
	return "", errors.New("herdrmux: shim not implemented")
}
