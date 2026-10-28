//go:build !windows

package herdrmux

import "fmt"

// placed is where a new session ended up in herdr.
type placed struct {
	WS    wsInfo
	TabID string
	Pane  string
	Kind  string
	Label string
}

// place puts a new session into herdr according to pl: a tab (or a linked
// worktree workspace) in the group workspace, creating the group on demand.
// The group record lock is held throughout so concurrent new-sessions share
// one group workspace and one claims the group's initial shell tab.
func (m *mux) place(pl Placement, cwd string) (placed, error) {
	var out placed
	err := m.store.UpdateGroups(func(g map[string]groupRec) error {
		wss, err := m.cl.listWorkspaces(m.ctx)
		if err != nil {
			return err
		}
		rec, tracked := g[pl.Group]
		var group *wsInfo
		if tracked {
			for i := range wss {
				if wss[i].ID == rec.WorkspaceID && groupLabelMatches(wss[i].Label, pl.Group) {
					group = &wss[i]
					break
				}
			}
		}
		groupLabel := GroupLabel(pl.Group)
		tabLabel := DisplayLabel(pl.Role, pl.Label)
		if group != nil && group.Label != groupLabel {
			// Legacy (or emoji-toggled) label: adopt the current one.
			if err := m.cl.renameWorkspace(m.ctx, group.ID, groupLabel); err == nil {
				group.Label = groupLabel
			}
		}
		if group == nil {
			c, err := m.cl.createWorkspace(m.ctx, groupLabel, pl.GroupCwd, nil)
			if err != nil {
				return err
			}
			rec = groupRec{WorkspaceID: c.WS.ID, FreeTab: c.TabID, FreePane: c.PaneID}
			g[pl.Group] = rec
			w := c.WS
			group = &w
		}

		if pl.Kind == KindWorktree {
			// Any failure (not a worktree of this repo, linked_worktree_source,
			// …) falls back to a plain tab.
			if c, err := m.cl.openWorktree(m.ctx, group.ID, cwd, tabLabel); err == nil && c.PaneID != "" && c.WS.ID != "" {
				out = placed{WS: c.WS, TabID: c.TabID, Pane: c.PaneID, Kind: KindWorktree, Label: pl.Label}
				return nil
			}
		}

		if rec.FreeTab != "" {
			tabs, err := m.cl.listTabs(m.ctx, group.ID)
			if err != nil {
				return err
			}
			free := false
			for _, t := range tabs {
				if t.ID == rec.FreeTab {
					free = true
				}
			}
			if free {
				if err := m.cl.renameTab(m.ctx, rec.FreeTab, tabLabel); err != nil {
					return err
				}
				out = placed{WS: *group, TabID: rec.FreeTab, Pane: rec.FreePane, Kind: KindTab, Label: pl.Label}
				rec.FreeTab, rec.FreePane = "", ""
				g[pl.Group] = rec
				return nil
			}
			rec.FreeTab, rec.FreePane = "", ""
			g[pl.Group] = rec
		}

		c, err := m.cl.createTab(m.ctx, group.ID, tabLabel, cwd)
		if err != nil {
			return err
		}
		if c.PaneID == "" || c.TabID == "" {
			return fmt.Errorf("herdr tab create returned no pane")
		}
		out = placed{WS: *group, TabID: c.TabID, Pane: c.PaneID, Kind: KindTab, Label: pl.Label}
		return nil
	})
	return out, err
}

// closePlaced closes what place created (best effort).
func (m *mux) closePlaced(p placed) {
	if p.Kind == KindWorktree {
		_ = m.cl.closeWorkspace(m.ctx, p.WS.ID)
		return
	}
	_ = m.cl.closeTab(m.ctx, p.TabID)
}

// closeSession closes the herdr object that backs a session: its tab, or the
// whole workspace for worktree and legacy sessions. A missing object is fine.
func (m *mux) closeSession(s *session) error {
	var err error
	if s.State.Kind == KindTab {
		err = m.cl.closeTab(m.ctx, s.State.TabID)
	} else {
		err = m.cl.closeWorkspace(m.ctx, s.WS.ID)
	}
	if err != nil && !isNotFound(err) {
		return err
	}
	return nil
}

// dropFreeTabs closes group workspaces' unclaimed initial shell tabs (they
// would otherwise keep an empty group open).
func (m *mux) dropFreeTabs() {
	for _, rec := range m.store.LoadGroups() {
		if rec.FreeTab != "" {
			_ = m.cl.closeTab(m.ctx, rec.FreeTab)
		}
	}
}
