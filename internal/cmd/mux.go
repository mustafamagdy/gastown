package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/herdrmux"
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
