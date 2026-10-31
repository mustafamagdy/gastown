// Package azurecred stores one Azure DevOps token per organization.
//
// A token belongs to an organization, not to a town, project or rig: every rig
// whose remote is in that organization gets it, and a rig in another
// organization never does. Tokens live in files under the user's config
// directory, readable only by the user, and never appear in arguments or logs.
package azurecred

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// orgName matches an Azure DevOps organization name. It also keeps the name
// safe to use as a file name.
var orgName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*$`)

// ValidOrg reports whether org is a well-formed organization name.
func ValidOrg(org string) bool { return orgName.MatchString(org) }

// Dir returns the directory holding the token files.
func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "gt", "azure"), nil
}

func tokenPath(org string) (string, error) {
	if !ValidOrg(org) {
		return "", fmt.Errorf("invalid Azure DevOps organization name %q", org)
	}
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	// Names are case-insensitive in Azure DevOps.
	return filepath.Join(dir, strings.ToLower(org)+".pat"), nil
}

// Save stores token for org, replacing any earlier one.
func Save(org, token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("empty token")
	}
	if strings.ContainsAny(token, "\r\n") {
		return errors.New("token must be a single line")
	}
	path, err := tokenPath(org)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".pat-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(token); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path) // CreateTemp makes the file 0600
}

// Load returns the token for org, or "" when none is stored.
func Load(org string) (string, error) {
	path, err := tokenPath(org)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is built from a validated org name
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// Delete removes the token for org. It reports whether one existed.
func Delete(org string) (bool, error) {
	path, err := tokenPath(org)
	if err != nil {
		return false, err
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// Entry describes a stored token without exposing it.
type Entry struct {
	Org     string
	Updated time.Time
}

// List returns the organizations that have a token, sorted by name.
func List() ([]Entry, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	files, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, f := range files {
		org, ok := strings.CutSuffix(f.Name(), ".pat")
		if !ok || !ValidOrg(org) {
			continue
		}
		info, err := f.Info()
		if err != nil {
			return nil, err
		}
		out = append(out, Entry{Org: org, Updated: info.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Org < out[j].Org })
	return out, nil
}

// orgFromRequest finds the organization in a git credential request.
func orgFromRequest(fields map[string]string) string {
	host := strings.ToLower(fields["host"])
	if org, ok := strings.CutSuffix(host, ".visualstudio.com"); ok {
		return org
	}
	if host != "dev.azure.com" {
		return ""
	}
	if first, _, _ := strings.Cut(strings.TrimPrefix(fields["path"], "/"), "/"); first != "" {
		return first
	}
	return fields["username"]
}

// AnswerGit runs the "get" action of the git credential helper protocol: it
// reads the request from in and writes a username and password to out. With no
// token for the organization it writes nothing, so git moves on to its next
// helper.
func AnswerGit(in io.Reader, out io.Writer) error {
	fields := map[string]string{}
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		if key, value, ok := strings.Cut(sc.Text(), "="); ok {
			fields[key] = value
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if fields["protocol"] != "https" {
		return nil
	}
	org := orgFromRequest(fields)
	if !ValidOrg(org) {
		return nil
	}
	token, err := Load(org)
	if err != nil || token == "" {
		return err
	}
	_, err = fmt.Fprintf(out, "username=pat\npassword=%s\n", token)
	return err
}
