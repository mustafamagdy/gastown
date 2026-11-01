package tracker

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// fakeRunner answers CLI calls from canned output keyed by a substring of the
// joined command line, and records every call.
type fakeRunner struct {
	mu    sync.Mutex
	calls []string
	reply map[string]string // substring -> stdout (first match in insertion order below)
	order []string
	fail  map[string]error
}

func newFake() *fakeRunner {
	return &fakeRunner{reply: map[string]string{}, fail: map[string]error{}}
}

func (f *fakeRunner) on(substr, out string) *fakeRunner {
	f.reply[substr] = out
	f.order = append(f.order, substr)
	return f
}

func (f *fakeRunner) run(_ context.Context, bin string, args ...string) ([]byte, error) {
	line := bin + " " + strings.Join(args, " ")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, line)
	for _, k := range f.order {
		if strings.Contains(line, k) {
			if err := f.fail[k]; err != nil {
				return nil, err
			}
			return []byte(f.reply[k]), nil
		}
	}
	return nil, fmt.Errorf("fake runner: unexpected call %s", line)
}

// writes returns the recorded calls that change the tracker.
func (f *fakeRunner) writes() []string {
	var w []string
	for _, c := range f.calls {
		if strings.Contains(c, "work-item update") || strings.Contains(c, "issue edit") || strings.Contains(c, "issue close") {
			w = append(w, c)
		}
	}
	return w
}
