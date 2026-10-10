package memory

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/stubbedev/harness/internal/fsext"
)

// repoKeyTimeout bounds the git calls that derive a repo key. They read
// local metadata only, so this is generous; a hung git (a network
// filesystem, a stuck lock) falls back to the path key instead of
// holding startup.
const repoKeyTimeout = 5 * time.Second

// caseInsensitiveHosts are forges that treat owner and repository names
// case-insensitively, so a clone of github.com/Foo/Bar and one of
// github.com/foo/bar are the same repository. Elsewhere the path keeps
// its case: a self-hosted server on a case-sensitive filesystem can host
// both, and merging them would be wrong where splitting them is merely
// a missed share.
var caseInsensitiveHosts = map[string]bool{
	"github.com":    true,
	"gitlab.com":    true,
	"bitbucket.org": true,
}

// RepoKey returns the key that identifies the repository dir belongs to,
// so every clone, worktree and subdirectory of one repository shares its
// repo-scoped memories, on this machine and on others:
//
//   - the upstream URL of remote "origin" (else the first remote by name),
//     normalized to host/path: "github.com/stubbedev/harness";
//   - without a remote, "root:" and the repository's root commit;
//   - outside a repository, or without git, "path:" and the canonical
//     project directory, the boundary the workspace data directory is
//     keyed by.
func RepoKey(ctx context.Context, dir string) string {
	ctx, cancel := context.WithTimeout(ctx, repoKeyTimeout)
	defer cancel()

	top, err := gitOutput(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil || top == "" {
		return pathKey(dir)
	}
	if raw := remoteURL(ctx, top); raw != "" {
		if key := normalizeRemote(raw, top); key != "" {
			return key
		}
	}
	if root := rootCommit(ctx, top); root != "" {
		return "root:" + root
	}
	return pathKey(top)
}

// pathKey is the key of a directory that is not in a usable repository.
func pathKey(dir string) string {
	if canonical, err := fsext.Canonicalize(dir); err == nil {
		dir = canonical
	}
	return "path:" + dir
}

// remoteURL returns the URL of the remote that names the upstream:
// origin when it exists, else the first remote in sorted order. It goes
// through "git remote get-url" so url.<base>.insteadOf rewrites apply,
// which turns a shorthand like "gh:owner/repo" into the URL it stands
// for.
func remoteURL(ctx context.Context, dir string) string {
	out, err := gitOutput(ctx, dir, "remote")
	if err != nil || out == "" {
		return ""
	}
	remotes := strings.Fields(out)
	slices.Sort(remotes)
	name := remotes[0]
	if slices.Contains(remotes, "origin") {
		name = "origin"
	}
	raw, err := gitOutput(ctx, dir, "remote", "get-url", name)
	if err != nil {
		return ""
	}
	return raw
}

// rootCommit returns the root commit of HEAD's history; with several
// roots (merged unrelated histories) the lexicographically smallest, so
// every clone picks the same one. Empty for a repository without
// commits.
func rootCommit(ctx context.Context, dir string) string {
	out, err := gitOutput(ctx, dir, "rev-list", "--max-parents=0", "HEAD")
	if err != nil {
		return ""
	}
	roots := strings.Fields(out)
	if len(roots) == 0 {
		return ""
	}
	return slices.Min(roots)
}

// normalizeRemote reduces a remote URL to host/path, so the transports
// one repository is reached by agree: scheme, user info and port are
// dropped, the scp-like "git@host:path" form is read as host and path,
// the host is lowercased, and a trailing ".git" or slash is trimmed.
// The port goes because it names the transport, not the repository: a
// forge serves the same repository over HTTPS on 443 and SSH on 2222.
// A local remote (a path, or a file:// URL) becomes "file:" and the
// cleaned absolute path, resolved against base when relative. Empty
// when nothing usable remains.
func normalizeRemote(raw, base string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	var host, path string
	switch {
	case strings.Contains(raw, "://"):
		u, err := url.Parse(raw)
		if err != nil {
			return ""
		}
		if u.Scheme == "file" {
			return fileKey(u.Path, base)
		}
		host, path = u.Hostname(), u.Path
	case isLocalRemote(raw):
		return fileKey(raw, base)
	default:
		// scp-like syntax: [user@]host:path. Git reads it so only when
		// the colon comes before any slash, which isLocalRemote checked.
		colon := strings.IndexByte(raw, ':')
		host, path = raw[:colon], raw[colon+1:]
		if at := strings.LastIndexByte(host, '@'); at >= 0 {
			host = host[at+1:]
		}
	}

	host = strings.ToLower(host)
	path = trimRepoPath(path)
	if host == "" || path == "" {
		return ""
	}
	if caseInsensitiveHosts[host] {
		path = strings.ToLower(path)
	}
	return host + "/" + path
}

// isLocalRemote reports whether a remote without a scheme names a local
// path rather than an scp-like host:path: git's own rule is that a colon
// before the first slash makes it scp-like, except for a Windows drive
// letter.
func isLocalRemote(raw string) bool {
	if len(raw) >= 2 && raw[1] == ':' && isASCIILetter(raw[0]) && (len(raw) == 2 || raw[2] == '/' || raw[2] == '\\') {
		return true
	}
	colon := strings.IndexByte(raw, ':')
	if colon < 0 {
		return true
	}
	slash := strings.IndexAny(raw, `/\`)
	return slash >= 0 && slash < colon
}

func isASCIILetter(b byte) bool {
	return 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z'
}

// fileKey is the key of a remote on the local filesystem.
func fileKey(path, base string) string {
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	path = filepath.ToSlash(filepath.Clean(path))
	// A Unix path keeps its leading slash; trimRepoPath would take it
	// along with the trailing ones.
	lead := ""
	if strings.HasPrefix(path, "/") {
		lead = "/"
	}
	path = trimRepoPath(path)
	if path == "" {
		return ""
	}
	return "file:" + lead + path
}

// trimRepoPath strips the slashes around a repository path and its
// ".git" suffix.
func trimRepoPath(path string) string {
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	return strings.Trim(path, "/")
}

// gitOutput runs git in dir and returns its trimmed stdout. Prompts and
// optional locks are off: this only reads, and must never wait on a
// credential helper or contend with the user's own git.
func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
