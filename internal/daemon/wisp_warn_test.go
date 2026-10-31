package daemon

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestIsRigOperational_WarnsOncePerRigForMissingWispConfig(t *testing.T) {
	var buf bytes.Buffer
	d := &Daemon{
		config: &Config{TownRoot: t.TempDir()},
		logger: log.New(&buf, "", 0),
	}
	for i := 0; i < 3; i++ {
		d.isRigOperational("alpha")
	}
	d.isRigOperational("beta")

	out := buf.String()
	if n := strings.Count(out, "no wisp config for alpha"); n != 1 {
		t.Errorf("alpha warned %d times, want 1", n)
	}
	if n := strings.Count(out, "no wisp config for beta"); n != 1 {
		t.Errorf("beta warned %d times, want 1", n)
	}
}
