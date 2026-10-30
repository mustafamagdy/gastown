package projectmap

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/steveyegge/gastown/internal/config"
)

// BuildOptions controls Build.
type BuildOptions struct {
	Force bool   // rescan rigs even when their commit is unchanged
	Rig   string // limit the rescan to one rig ("" for all)
}

// RigResult reports what Build did for one rig.
type RigResult struct {
	Rig    string
	Status string // "scanned", "unchanged" or "skipped"
	Err    error  // why a rig was skipped, if it was an error
}

// Build refreshes a project's service map and saves it when something
// changed. Rigs whose default branch tip is unchanged since the last build are
// not rescanned unless forced; an override file newer than the map also forces
// a rescan of its rig. A rig that cannot be scanned keeps its previous entry.
// Rigs that left the project are dropped.
func Build(townRoot, project string, opts BuildOptions) (*Map, []RigResult, error) {
	rigsConfig, err := config.LoadRigsConfig(filepath.Join(townRoot, "mayor", "rigs.json"))
	if err != nil {
		return nil, nil, fmt.Errorf("loading rigs config: %w", err)
	}
	rigs := config.RigsInProject(rigsConfig, project)
	if opts.Rig != "" {
		found := false
		for _, r := range rigs {
			found = found || r == opts.Rig
		}
		if !found {
			return nil, nil, fmt.Errorf("rig %q is not in project %q", opts.Rig, project)
		}
	}

	prevMap, err := Load(townRoot, project)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		prevMap = nil // unreadable map: rebuild from scratch
	}
	prev := map[string]Service{}
	var prevTime time.Time
	if prevMap != nil {
		prevTime = prevMap.GeneratedAt
		for _, s := range prevMap.Services {
			prev[s.Rig] = s
		}
	}

	m := &Map{Version: CurrentVersion, Project: project, GeneratedAt: time.Now().UTC()}
	var results []RigResult
	changed := prevMap == nil || len(prev) != len(rigs)
	for _, rig := range rigs {
		repo := repoName(rigsConfig.Rigs[rig].GitURL)
		rigPath := filepath.Join(townRoot, rig)
		bare := filepath.Join(rigPath, ".repo.git")
		branch := rigDefaultBranch(rigPath)
		overridePath := filepath.Join(OverrideDir(townRoot, project), rig+".toml")
		old, have := prev[rig]
		keep := func(status string, err error) {
			results = append(results, RigResult{Rig: rig, Status: status, Err: err})
			if have {
				m.Services = append(m.Services, old)
			}
		}

		if opts.Rig != "" && opts.Rig != rig {
			if have {
				m.Services = append(m.Services, old)
			}
			continue
		}
		commit, err := refCommit(bare, branch)
		if err != nil {
			keep("skipped", err)
			continue
		}
		if have && !opts.Force && old.Commit == commit && old.Repo == repo && !modifiedSince(overridePath, prevTime) {
			keep("unchanged", nil)
			continue
		}
		svc, err := scanRig(rig, repo, bare, branch, overridePath)
		if err != nil {
			keep("skipped", err)
			continue
		}
		results = append(results, RigResult{Rig: rig, Status: "scanned"})
		m.Services = append(m.Services, svc)
		changed = true
	}
	normalizeCalls(m.Services)
	sortServices(m)
	if !changed {
		return prevMap, results, nil
	}
	if err := Save(townRoot, m); err != nil {
		return nil, results, err
	}
	return m, results, nil
}

// modifiedSince reports whether the file exists and is newer than t.
func modifiedSince(path string, t time.Time) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.ModTime().After(t)
}
