package formula

import (
	"strings"
	"testing"
)

// PR mode must go through the gt mq pr verbs (internal/vcs), so a rig on Azure
// DevOps works like one on GitHub. The formula must not call gh or az itself.
func TestRefineryPatrolPRModeUsesGtVerbs(t *testing.T) {
	f := loadRefineryPatrolFormula(t)
	step := requireFormulaStep(t, f, "merge-push")

	for _, want := range []string{
		"gt mq pr create <rig>",
		"gt mq pr checks <rig>",
		"gt mq pr review <rig>",
		"gt mq pr merge <rig>",
		"--expect-head",
	} {
		if !strings.Contains(step.Description, want) {
			t.Errorf("merge-push lacks %q", want)
		}
	}

	content, err := formulasFS.ReadFile("formulas/mol-refinery-patrol.formula.toml")
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		for _, bad := range []string{"gh pr create", "gh pr checks", "gh pr merge", "gh pr view", "az repos"} {
			// The merged-PR sweep warning names gh only to forbid it.
			if strings.Contains(trimmed, bad) && !strings.Contains(trimmed, "Do NOT") {
				t.Errorf("line %d still calls %q: %s", i+1, bad, trimmed)
			}
		}
	}
}
