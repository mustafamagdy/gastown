package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/herdrmux"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/workspace"
)

// annotationMux marks the herdr multiplexer plumbing commands. They run on
// hot paths (every tmux call under the herdr backend) and from arbitrary
// directories, so they skip telemetry, town discovery and all pre-run checks.
const annotationMux = "gt.mux"

var (
	muxSuperviseSock    string
	muxSuperviseSession string
)

var muxTmuxCmd = &cobra.Command{
	Use:                "mux-tmux [tmux args...]",
	Short:              "Run a tmux command against the herdr backend",
	Long:               "Emulates the tmux CLI on top of herdr. Used internally when the herdr multiplexer backend is selected.",
	Hidden:             true,
	DisableFlagParsing: true,
	SilenceUsage:       true,
	SilenceErrors:      true,
	Annotations:        map[string]string{annotationMux: "true"},
	Run: func(cmd *cobra.Command, args []string) {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		args = muxResolveSocketArgs(args, os.Getenv, muxFindTownSocket)
		code := herdrmux.Run(ctx, args, os.Stdin, os.Stdout, os.Stderr)
		stop()
		os.Exit(code)
	},
}

var muxSuperviseCmd = &cobra.Command{
	Use:           "mux-supervise --sock S --session N",
	Short:         "Supervise a pane process for the herdr backend",
	Long:          "Runs inside a herdr pane and manages the session's child process. Used internally.",
	Hidden:        true,
	SilenceUsage:  true,
	SilenceErrors: true,
	Annotations:   map[string]string{annotationMux: "true"},
	Run: func(cmd *cobra.Command, args []string) {
		os.Exit(herdrmux.Supervise(context.Background(), muxSuperviseSock, muxSuperviseSession))
	},
}

var muxShimPathCmd = &cobra.Command{
	Use:           "mux-shim-path",
	Short:         "Print the directory containing the tmux shim for the herdr backend",
	Hidden:        true,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	Annotations:   map[string]string{annotationMux: "true"},
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := herdrmux.EnsureShim()
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), dir)
		return nil
	},
}

func init() {
	muxSuperviseCmd.Flags().StringVar(&muxSuperviseSock, "sock", "", "herdr session / tmux socket name")
	muxSuperviseCmd.Flags().StringVar(&muxSuperviseSession, "session", "", "tmux session name")
	_ = muxSuperviseCmd.MarkFlagRequired("sock")
	_ = muxSuperviseCmd.MarkFlagRequired("session")
	rootCmd.AddCommand(muxTmuxCmd, muxSuperviseCmd, muxShimPathCmd)
}

// isMuxCommand reports whether cmd is one of the herdr mux plumbing commands.
func isMuxCommand(cmd *cobra.Command) bool {
	return cmd != nil && cmd.Annotations[annotationMux] == "true"
}

// isMuxInvocation reports whether the given CLI args invoke a mux command.
func isMuxInvocation(args []string) bool {
	cmd, _, err := rootCmd.Find(args)
	return err == nil && isMuxCommand(cmd)
}

// muxFindTownSocket cheaply locates the town from the cwd and returns its
// tmux socket name, or "" if not inside a town. No beads/dolt access.
func muxFindTownSocket() string {
	root, err := workspace.FindFromCwd()
	if err != nil || root == "" {
		return ""
	}
	return session.ResolveTownSocket(root)
}

// muxResolveSocketArgs prepends "-L <town socket>" to mux-tmux argv when the
// caller gave neither -L nor -S and is not inside a tmux/herdr pane (TMUX
// unset). Mux commands skip town discovery for speed, so a plain shell would
// otherwise target the gt-default session instead of the town's. The socket
// is GT_TOWN_SOCKET if set, else findTown().
func muxResolveSocketArgs(args []string, getenv func(string) string, findTown func() string) []string {
	if getenv("TMUX") != "" {
		return args
	}
loop:
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-L", "-S":
			return args
		case "-f", "-c", "-T":
			i++
		case "-u", "-2", "-C", "-CC", "-D", "-l", "-N", "-v", "-vv", "-V":
		default:
			break loop
		}
	}
	sock := getenv("GT_TOWN_SOCKET")
	if sock == "" {
		sock = findTown()
	}
	if sock == "" {
		return args
	}
	return append([]string{"-L", sock}, args...)
}
