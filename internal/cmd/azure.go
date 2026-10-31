package cmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/steveyegge/gastown/internal/azurecred"
	"github.com/steveyegge/gastown/internal/style"
)

// azureCredentialURLs are the git credential scopes the helper answers for.
var azureCredentialURLs = []string{"https://dev.azure.com", "https://*.visualstudio.com"}

var azureCmd = &cobra.Command{
	Use:     "azure",
	GroupID: GroupServices,
	Short:   "Manage Azure DevOps tokens, one per organization",
	Long: `Manage Azure DevOps tokens. A token belongs to an organization: once saved,
every rig, project and town with a remote in that organization uses it, and so
do git, az and the daemon. Other organizations never see it.

Some organizations reject the az login token for git and REST calls
(TF400813), so a personal access token with Code read and write is required.`,
	RunE: requireSubcommand,
}

var azureLoginCmd = &cobra.Command{
	Use:   "login <org>",
	Short: "Save a token for an organization",
	Long: `Save a personal access token for an Azure DevOps organization.

The token is read from the terminal without echo, or from stdin when piped. It
is never accepted as an argument. It is checked against Azure before it is
saved, then stored for git and for az.

Examples:
  gt azure login geeksclub
  gt azure login geeksclub < token-file`,
	Args: cobra.ExactArgs(1),
	RunE: runAzureLogin,
}

var azureLogoutCmd = &cobra.Command{
	Use:   "logout <org>",
	Short: "Remove the token for an organization",
	Args:  cobra.ExactArgs(1),
	RunE:  runAzureLogout,
}

var azureStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "List organizations that have a token",
	Args:  cobra.NoArgs,
	RunE:  runAzureStatus,
}

// azureGitCredentialCmd is what git runs. The mux annotation skips the shared
// pre-run: it must stay fast, and extra output could break the protocol.
var azureGitCredentialCmd = &cobra.Command{
	Use:         "git-credential <get|store|erase>",
	Short:       "Git credential helper for Azure DevOps",
	Hidden:      true,
	Annotations: map[string]string{annotationMux: "true"},
	Args:        cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if args[0] != "get" {
			return nil
		}
		return azurecred.AnswerGit(cmd.InOrStdin(), cmd.OutOrStdout())
	},
}

func init() {
	azureCmd.AddCommand(azureLoginCmd, azureLogoutCmd, azureStatusCmd, azureGitCredentialCmd)
	rootCmd.AddCommand(azureCmd)
}

func readAzureToken() (string, error) {
	fd := int(os.Stdin.Fd()) //nolint:gosec // G115: file descriptors fit in int
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, "Personal access token: ")
		data, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return strings.TrimSpace(string(data)), err
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 4096))
	return strings.TrimSpace(string(data)), err
}

func azureOrgURL(org string) string { return "https://dev.azure.com/" + org }

// azWithToken runs az and passes token on stdin, keeping it off the command line.
func azWithToken(ctx context.Context, token string, args ...string) error {
	c := exec.CommandContext(ctx, "az", args...) //nolint:gosec // G204: fixed binary, args are exec args
	c.Stdin = strings.NewReader(token + "\n")
	var stderr bytes.Buffer
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// installAzureCredentialHelper points git at this binary for Azure hosts only.
// It is idempotent and leaves helpers for other hosts alone.
func installAzureCredentialHelper() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	helper := exe + " azure git-credential"
	for _, url := range azureCredentialURLs {
		for key, value := range map[string]string{
			"credential." + url + ".helper":      helper,
			"credential." + url + ".useHttpPath": "true",
		} {
			out, err := exec.Command("git", "config", "--global", "--replace-all", key, value).CombinedOutput() //nolint:gosec // G204: fixed binary and key shape
			if err != nil {
				return fmt.Errorf("git config %s: %w: %s", key, err, strings.TrimSpace(string(out)))
			}
		}
	}
	return nil
}

func runAzureLogin(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	org := strings.ToLower(args[0])
	if !azurecred.ValidOrg(org) {
		return fmt.Errorf("invalid organization name %q", args[0])
	}
	token, err := readAzureToken()
	if err != nil {
		return fmt.Errorf("reading token: %w", err)
	}
	if token == "" {
		return fmt.Errorf("no token given")
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
	defer cancel()
	if err := azWithToken(ctx, token, "devops", "login", "--organization", azureOrgURL(org)); err != nil {
		return fmt.Errorf("az rejected the token for %s: %w", org, err)
	}
	if err := azurecred.Save(org, token); err != nil {
		return fmt.Errorf("saving token: %w", err)
	}
	if err := installAzureCredentialHelper(); err != nil {
		return err
	}
	fmt.Printf("%s Saved token for %s; git, az and the daemon will use it\n", style.Bold.Render("✓"), org)
	return nil
}

func runAzureLogout(cmd *cobra.Command, args []string) error {
	org := strings.ToLower(args[0])
	if !azurecred.ValidOrg(org) {
		return fmt.Errorf("invalid organization name %q", args[0])
	}
	existed, err := azurecred.Delete(org)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), time.Minute)
	defer cancel()
	azErr := exec.CommandContext(ctx, "az", "devops", "logout", "--organization", azureOrgURL(org)).Run() //nolint:gosec // G204: fixed binary, org validated above
	if !existed && azErr != nil {
		fmt.Printf("No token stored for %s\n", org)
		return nil
	}
	if azErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: az devops logout failed: %v\n", azErr)
	}
	fmt.Printf("%s Removed token for %s\n", style.Bold.Render("✓"), org)
	return nil
}

func runAzureStatus(cmd *cobra.Command, args []string) error {
	entries, err := azurecred.List()
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Println("No Azure DevOps tokens stored. Add one with: gt azure login <org>")
		return nil
	}
	for _, e := range entries {
		fmt.Printf("%-24s saved %s\n", e.Org, e.Updated.Format("2006-01-02"))
	}
	return nil
}
