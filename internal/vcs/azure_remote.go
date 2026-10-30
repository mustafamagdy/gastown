package vcs

import (
	"fmt"
	"net/url"
	"strings"
)

// AzureLocation identifies an Azure DevOps repository.
type AzureLocation struct {
	OrgURL  string // https://dev.azure.com/<org> or https://<org>.visualstudio.com
	Project string
	Repo    string
}

// ParseAzureRemote extracts organization, project and repository from an
// Azure DevOps git remote. Supported forms:
//
//	https://dev.azure.com/<org>/<project>/_git/<repo>   (user info is dropped)
//	https://<org>@dev.azure.com/<org>/<project>/_git/<repo>
//	git@ssh.dev.azure.com:v3/<org>/<project>/<repo>
//	ssh://git@ssh.dev.azure.com/v3/<org>/<project>/<repo>
//	https://<org>.visualstudio.com/[DefaultCollection/]<project>/_git/<repo>
//	<org>@vs-ssh.visualstudio.com:v3/<org>/<project>/<repo>
//
// A URL of the form .../<org>/_git/<repo> (no project) uses the repo name as
// the project, which is how Azure DevOps treats default repositories.
func ParseAzureRemote(remote string) (AzureLocation, error) {
	host, path := splitRemote(remote)
	if host == "" {
		return AzureLocation{}, fmt.Errorf("not an Azure DevOps remote: %q", RedactURL(remote))
	}
	segs := splitPath(path)
	bad := func() (AzureLocation, error) {
		return AzureLocation{}, fmt.Errorf("cannot parse Azure DevOps remote %q", RedactURL(remote))
	}
	switch {
	case host == "ssh.dev.azure.com" || host == "vs-ssh.visualstudio.com":
		if len(segs) != 4 || segs[0] != "v3" {
			return bad()
		}
		org := segs[1]
		orgURL := "https://dev.azure.com/" + org
		if host == "vs-ssh.visualstudio.com" {
			orgURL = "https://" + org + ".visualstudio.com"
		}
		return AzureLocation{OrgURL: orgURL, Project: segs[2], Repo: segs[3]}, nil
	case host == "dev.azure.com":
		if len(segs) < 3 {
			return bad()
		}
		org := segs[0]
		return azureFromGitPath("https://dev.azure.com/"+org, segs[1:], bad)
	case strings.HasSuffix(host, ".visualstudio.com"):
		org := strings.TrimSuffix(host, ".visualstudio.com")
		rest := segs
		if len(rest) > 0 && strings.EqualFold(rest[0], "DefaultCollection") {
			rest = rest[1:]
		}
		return azureFromGitPath("https://"+org+".visualstudio.com", rest, bad)
	}
	return bad()
}

// azureFromGitPath handles [<project>/]_git/<repo>.
func azureFromGitPath(orgURL string, segs []string, bad func() (AzureLocation, error)) (AzureLocation, error) {
	switch {
	case len(segs) == 3 && segs[1] == "_git":
		return AzureLocation{OrgURL: orgURL, Project: segs[0], Repo: segs[2]}, nil
	case len(segs) == 2 && segs[0] == "_git":
		return AzureLocation{OrgURL: orgURL, Project: segs[1], Repo: segs[1]}, nil
	}
	return bad()
}

// remoteHost returns the lower-case host of a git remote URL (https, ssh://,
// or scp-like user@host:path).
func remoteHost(remote string) string {
	h, _ := splitRemote(remote)
	return h
}

// splitRemote returns the host and the path of a remote, user info removed.
func splitRemote(remote string) (host, path string) {
	remote = strings.TrimSpace(remote)
	if strings.Contains(remote, "://") {
		u, err := url.Parse(remote)
		if err != nil {
			return "", ""
		}
		return strings.ToLower(u.Hostname()), u.Path
	}
	// scp-like: [user@]host:path
	at := strings.LastIndex(remote, "@")
	rest := remote[at+1:]
	i := strings.Index(rest, ":")
	if i <= 0 {
		return "", ""
	}
	return strings.ToLower(rest[:i]), rest[i+1:]
}

func splitPath(p string) []string {
	var out []string
	for _, s := range strings.Split(strings.Trim(p, "/"), "/") {
		if s == "" {
			continue
		}
		if d, err := url.PathUnescape(s); err == nil {
			s = d
		}
		out = append(out, strings.TrimSuffix(s, ".git"))
	}
	return out
}

// RedactURL drops user info (which can hold a token) from a URL for messages.
func RedactURL(raw string) string {
	if strings.Contains(raw, "://") {
		if u, err := url.Parse(raw); err == nil {
			u.User = nil
			return u.String()
		}
	}
	return raw
}

// normalizeRef strips refs/heads/ so branch names compare equal.
func normalizeRef(ref string) string {
	return strings.TrimPrefix(ref, "refs/heads/")
}
