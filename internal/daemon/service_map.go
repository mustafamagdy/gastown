package daemon

import (
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/projectmap"
)

// serviceMapInterval is the minimum time between service map refreshes of one project.
const serviceMapInterval = 5 * time.Minute

// refreshServiceMaps rebuilds each project's service map incrementally. A
// rig is rescanned only when its default branch moved (one git rev-parse per
// rig otherwise). Errors are logged and never fail the heartbeat.
func (d *Daemon) refreshServiceMaps() {
	projects, err := config.ListProjects(d.config.TownRoot)
	if err != nil {
		d.logger.Printf("service_map: listing projects: %v", err)
		return
	}
	if d.serviceMapLastRun == nil {
		d.serviceMapLastRun = make(map[string]time.Time)
	}
	for _, project := range projects {
		if time.Since(d.serviceMapLastRun[project]) < serviceMapInterval {
			continue
		}
		d.serviceMapLastRun[project] = time.Now()
		_, results, err := projectmap.Build(d.config.TownRoot, project, projectmap.BuildOptions{})
		if err != nil {
			d.logger.Printf("service_map: %s: %v", project, err)
			continue
		}
		for _, r := range results {
			switch {
			case r.Err != nil:
				d.logger.Printf("service_map: %s/%s skipped: %v", project, r.Rig, r.Err)
			case r.Status == "scanned":
				d.logger.Printf("service_map: %s/%s updated", project, r.Rig)
			}
		}
	}
}
