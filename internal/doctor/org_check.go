package doctor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/config"
)

// OrgEnvCheck verifies the town's org_env setting resolves to some variables.
type OrgEnvCheck struct {
	BaseCheck
}

// NewOrgEnvCheck creates the org environment check.
func NewOrgEnvCheck() *OrgEnvCheck {
	return &OrgEnvCheck{
		BaseCheck: BaseCheck{
			CheckName:        "org-env",
			CheckDescription: "Verify the town's org_env setting resolves to environment variables",
			CheckCategory:    CategoryConfig,
		},
	}
}

// Run implements Check.
func (c *OrgEnvCheck) Run(ctx *CheckContext) *CheckResult {
	settings, err := config.LoadOrCreateTownSettings(config.TownSettingsPath(ctx.TownRoot))
	if err != nil {
		return &CheckResult{Name: c.Name(), Status: StatusWarning,
			Message: "could not load town settings", Details: []string{err.Error()}}
	}
	if settings.OrgEnv == nil {
		return &CheckResult{Name: c.Name(), Status: StatusOK, Message: "not configured (skipped)"}
	}
	env, err := config.ResolveOrgEnvConfig(settings.OrgEnv, os.Environ())
	if err != nil {
		return &CheckResult{Name: c.Name(), Status: StatusWarning,
			Message: "org_env does not resolve", Details: []string{err.Error()},
			FixHint: "Agents start without the org environment. Fix settings/config.json org_env (run 'gt org env')"}
	}
	if len(env.Vars) == 0 {
		return &CheckResult{Name: c.Name(), Status: StatusWarning,
			Message: fmt.Sprintf("org_env from %s yields no variables", env.Source),
			FixHint: "Check the source defines the variables, and the \"vars\" allow-list names match"}
	}
	return &CheckResult{Name: c.Name(), Status: StatusOK,
		Message: fmt.Sprintf("%d variables from %s", len(env.Vars), env.Source)}
}

// axiHomeTimeout bounds one AXI tool's no-arg home view.
const axiHomeTimeout = 15 * time.Second

// AxiRunner runs a tool and returns its combined output. dir is the working
// directory and env the full environment.
type AxiRunner func(ctx context.Context, dir string, env []string, bin string, args ...string) ([]byte, error)

func defaultAxiRunner(ctx context.Context, dir string, env []string, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = env
	return cmd.CombinedOutput()
}

// AxiToolsCheck verifies each AXI tool in the town's axi_tools setting is
// installed, at its pinned version, and that its home view runs.
type AxiToolsCheck struct {
	BaseCheck
	// run executes a tool; nil uses the real exec. For tests.
	run AxiRunner
}

// NewAxiToolsCheck creates the AXI tools check.
func NewAxiToolsCheck() *AxiToolsCheck {
	return &AxiToolsCheck{
		BaseCheck: BaseCheck{
			CheckName:        "axi-tools",
			CheckDescription: "Verify configured AXI tools are installed, at the pinned version, and run",
			CheckCategory:    CategoryConfig,
		},
	}
}

func envValue(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], key+"="); ok {
			return v
		}
	}
	return ""
}

func findInPath(name, pathVar string) (string, bool) {
	for _, dir := range filepath.SplitList(pathVar) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, true
		}
	}
	return "", false
}

// versionMatches reports whether output names the pinned version as a token.
func versionMatches(output, want string) bool {
	for _, tok := range strings.FieldsFunc(output, func(r rune) bool {
		return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '.' || r == '-' || r == '+')
	}) {
		if tok == want || tok == "v"+want {
			return true
		}
	}
	return false
}

// firstErrorLine returns the first non-empty line of a tool's output, or the
// error text when the output is empty.
func firstErrorLine(out []byte, err error) string {
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			if len(l) > 200 {
				l = l[:200] + "..."
			}
			return l
		}
	}
	return err.Error()
}

// Run implements Check.
func (c *AxiToolsCheck) Run(ctx *CheckContext) *CheckResult {
	settings, err := config.LoadOrCreateTownSettings(config.TownSettingsPath(ctx.TownRoot))
	if err != nil {
		return &CheckResult{Name: c.Name(), Status: StatusWarning,
			Message: "could not load town settings", Details: []string{err.Error()}}
	}
	if len(settings.AxiTools) == 0 {
		return &CheckResult{Name: c.Name(), Status: StatusOK, Message: "not configured (skipped)"}
	}

	env := os.Environ()
	var notes []string
	if settings.OrgEnv != nil {
		if org, err := config.ResolveOrgEnvConfig(settings.OrgEnv, env); err != nil {
			notes = append(notes, "org env not applied: "+err.Error())
		} else {
			env = org.Environ(env)
		}
	}
	run := c.run
	if run == nil {
		run = defaultAxiRunner
	}
	pathVar := envValue(env, "PATH")

	var problems []string
	for _, entry := range settings.AxiTools {
		name, pin := config.ParseAxiTool(entry)
		if name == "" {
			problems = append(problems, fmt.Sprintf("%q: empty tool name", entry))
			continue
		}
		bin, ok := findInPath(name, pathVar)
		if !ok {
			problems = append(problems, name+": not found on PATH")
			continue
		}
		if pin != "" {
			vctx, cancel := context.WithTimeout(context.Background(), axiHomeTimeout)
			out, err := run(vctx, ctx.TownRoot, env, bin, "--version")
			cancel()
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s: --version failed: %s", name, firstErrorLine(out, err)))
				continue
			}
			if !versionMatches(string(out), pin) {
				problems = append(problems, fmt.Sprintf("%s: version %q, pinned %s", name, strings.TrimSpace(firstErrorLine(out, nil)), pin))
				continue
			}
		}
		hctx, cancel := context.WithTimeout(context.Background(), axiHomeTimeout)
		out, err := run(hctx, ctx.TownRoot, env, bin)
		timedOut := hctx.Err() != nil
		cancel()
		if timedOut {
			problems = append(problems, fmt.Sprintf("%s: home view timed out after %s", name, axiHomeTimeout))
		} else if err != nil {
			problems = append(problems, fmt.Sprintf("%s: home view failed: %s", name, firstErrorLine(out, err)))
		}
	}

	if len(problems) > 0 {
		return &CheckResult{Name: c.Name(), Status: StatusError,
			Message: fmt.Sprintf("%d of %d AXI tools failing", len(problems), len(settings.AxiTools)),
			Details: append(problems, notes...),
			FixHint: "Install pinned tools with contrib/fork-host/install-axi.sh; fix the tool's own error (for example a stale Azure subscription)"}
	}
	r := &CheckResult{Name: c.Name(), Status: StatusOK,
		Message: fmt.Sprintf("%d AXI tools OK", len(settings.AxiTools))}
	r.Details = notes
	return r
}
