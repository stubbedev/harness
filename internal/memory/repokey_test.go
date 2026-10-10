package memory

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeRemote(t *testing.T) {
	t.Parallel()

	base := filepath.FromSlash("/work/repo")
	cases := []struct {
		name, raw, want string
	}{
		{"scp-like ssh", "git@github.com:stubbedev/harness.git", "github.com/stubbedev/harness"},
		{"https", "https://github.com/stubbedev/harness", "github.com/stubbedev/harness"},
		{"ssh url", "ssh://git@github.com/stubbedev/harness.git", "github.com/stubbedev/harness"},
		{"trailing slash", "https://github.com/stubbedev/harness/", "github.com/stubbedev/harness"},
		{"git suffix and slash", "https://github.com/stubbedev/harness.git/", "github.com/stubbedev/harness"},
		{"user info", "https://user:token@github.com/stubbedev/harness.git", "github.com/stubbedev/harness"},
		{"https port", "https://git.example.com:8443/team/tool.git", "git.example.com/team/tool"},
		{"ssh port", "ssh://git@git.example.com:2222/team/tool.git", "git.example.com/team/tool"},
		{"git protocol", "git://git.example.com/team/tool", "git.example.com/team/tool"},
		{"git+ssh scheme", "git+ssh://git@git.example.com/team/tool.git", "git.example.com/team/tool"},
		{"uppercase host", "https://GitHub.com/stubbedev/harness", "github.com/stubbedev/harness"},
		{"case-insensitive forge path", "git@github.com:StubbeDev/Harness.git", "github.com/stubbedev/harness"},
		{"case-sensitive host keeps path case", "https://Git.Example.com/Team/Tool.git", "git.example.com/Team/Tool"},
		{"gitlab subgroups", "git@gitlab.com:Group/Sub/Project.git", "gitlab.com/group/sub/project"},
		{"gitlab subgroups https", "https://gitlab.com/group/sub/project", "gitlab.com/group/sub/project"},
		{"scp-like without user", "example.com:team/tool.git", "example.com/team/tool"},
		{"file url", "file:///srv/git/tool.git", "file:/srv/git/tool"},
		{"absolute path", "/srv/git/tool.git", "file:/srv/git/tool"},
		{"relative path", "../tool.git", "file:/work/tool"},
		{"empty", "  ", ""},
		{"host only", "https://github.com/", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if runtime.GOOS == "windows" && strings.HasPrefix(tc.want, "file:") {
				t.Skip("local remote paths are Unix-shaped in this table")
			}
			require.Equal(t, tc.want, normalizeRemote(tc.raw, base))
		})
	}
}

func TestRepoKeyFromRemote(t *testing.T) {
	t.Parallel()
	requireGit(t)

	repo := newRepo(t)
	git(t, repo, "remote", "add", "upstream", "https://example.com/zeta/tool.git")
	git(t, repo, "remote", "add", "fork", "git@example.com:alpha/tool.git")
	require.Equal(t, "example.com/alpha/tool", RepoKey(t.Context(), repo), "without origin the first remote by name wins")

	git(t, repo, "remote", "add", "origin", "git@github.com:StubbeDev/Harness.git")
	require.Equal(t, "github.com/stubbedev/harness", RepoKey(t.Context(), repo), "origin wins")
}

func TestRepoKeySharedByWorktreesAndSubdirectories(t *testing.T) {
	t.Parallel()
	requireGit(t)

	repo := newRepo(t)
	sub := filepath.Join(repo, "a", "b")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	worktree := filepath.Join(t.TempDir(), "wt")
	git(t, repo, "worktree", "add", "-q", "-b", "side", worktree)

	// Without a remote the root commit identifies the repository.
	key := RepoKey(t.Context(), repo)
	require.True(t, strings.HasPrefix(key, "root:"), key)
	require.Equal(t, "root:"+git(t, repo, "rev-list", "--max-parents=0", "HEAD"), key)
	require.Equal(t, key, RepoKey(t.Context(), sub))
	require.Equal(t, key, RepoKey(t.Context(), worktree))

	// A remote takes over for every checkout at once: remotes live in
	// the shared config.
	git(t, repo, "remote", "add", "origin", "https://example.com/team/tool")
	require.Equal(t, "example.com/team/tool", RepoKey(t.Context(), worktree))
	require.Equal(t, "example.com/team/tool", RepoKey(t.Context(), sub))
}

func TestRepoKeySameUpstreamDifferentClones(t *testing.T) {
	t.Parallel()
	requireGit(t)

	one, two := newRepo(t), newRepo(t)
	git(t, one, "remote", "add", "origin", "git@github.com:stubbedev/harness.git")
	git(t, two, "remote", "add", "origin", "https://github.com/stubbedev/harness")
	require.Equal(t, RepoKey(t.Context(), one), RepoKey(t.Context(), two))
}

func TestRepoKeyRootCommitPicksSmallestRoot(t *testing.T) {
	t.Parallel()
	requireGit(t)

	repo := newRepo(t)
	first := git(t, repo, "rev-parse", "HEAD")
	git(t, repo, "checkout", "-q", "--orphan", "other")
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "other root")
	second := git(t, repo, "rev-parse", "HEAD")
	git(t, repo, "checkout", "-q", "main")
	git(t, repo, "merge", "-q", "--allow-unrelated-histories", "-m", "merge", "other")

	require.Equal(t, "root:"+min(first, second), RepoKey(t.Context(), repo))
}

func TestRepoKeyOutsideRepository(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	canonical, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	require.Equal(t, "path:"+canonical, RepoKey(t.Context(), dir))
}

func TestRepoKeyRepositoryWithoutCommits(t *testing.T) {
	t.Parallel()
	requireGit(t)

	dir := t.TempDir()
	git(t, dir, "init", "-q")
	canonical, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	require.Equal(t, "path:"+canonical, RepoKey(t.Context(), filepath.Join(dir)))
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

// newRepo creates a repository with one commit and no remote.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "commit", "-q", "--allow-empty", "-m", "root")
	return dir
}

// git runs git in dir with an isolated identity and config, returning
// its trimmed output.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}
