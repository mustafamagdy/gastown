package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/projectfeature"
)

// featureRunInterval is the minimum time between runs for one feature.
const featureRunInterval = time.Minute

// featureRunTimeout bounds one 'gt project feature run'.
const featureRunTimeout = 4 * time.Minute

// dueFeatures returns the open features that have not been run within
// featureRunInterval and marks them as run now. Reads only feature records
// (no beads), so it is cheap on every heartbeat.
func (d *Daemon) dueFeatures(now time.Time) []string {
	recs, errs := projectfeature.ListAll(d.config.TownRoot)
	for _, err := range errs {
		d.logger.Printf("project_feature: %v", err)
	}
	if d.featureLastRun == nil {
		d.featureLastRun = make(map[string]time.Time)
	}
	var due []string
	for _, rec := range recs {
		if rec.State != projectfeature.StateOpen || len(rec.Tasks) == 0 {
			continue
		}
		if now.Sub(d.featureLastRun[rec.ID]) < featureRunInterval {
			continue
		}
		d.featureLastRun[rec.ID] = now
		due = append(due, rec.ID)
	}
	return due
}

// advanceFeatures slings newly ready tasks of open multi-repo features. It
// shells out to 'gt project feature run --if-started', which only acts on a
// feature that already has a started task, so the first task stays an
// explicit decision. Runs in the background, one batch at a time; errors are
// logged and never fail the heartbeat.
func (d *Daemon) advanceFeatures() {
	due := d.dueFeatures(time.Now())
	if len(due) == 0 {
		return
	}
	if !d.featureRunning.CompareAndSwap(false, true) {
		return // previous batch still running
	}
	go func() {
		defer d.featureRunning.Store(false)
		for _, id := range due {
			d.runFeature(id)
		}
	}()
}

func (d *Daemon) runFeature(id string) {
	run := d.featureRunFn
	if run == nil {
		run = d.execFeatureRun
	}
	out, err := run(id)
	if err != nil {
		d.logger.Printf("project_feature: run %s failed: %v (output: %s)", id, err, strings.TrimSpace(string(out)))
	} else if s := strings.TrimSpace(string(out)); s != "" {
		d.logger.Printf("project_feature: run %s: %s", id, s)
	}
}

func (d *Daemon) execFeatureRun(id string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), featureRunTimeout)
	defer cancel()
	gt := d.gtPath
	if gt == "" {
		gt = "gt"
	}
	cmd := exec.CommandContext(ctx, gt, "project", "feature", "run", id, "--if-started")
	setSysProcAttr(cmd)
	cmd.Dir = d.config.TownRoot
	cmd.Env = append(beads.BuildMutationRoutingBDEnv(os.Environ(), filepath.Join(d.config.TownRoot, ".beads")), "GT_DAEMON=1")
	return cmd.CombinedOutput()
}
