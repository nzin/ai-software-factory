// Package workspace manages the per-run working directory that developer agents
// write into and reviewers inspect. A run targets a repository (local or remote,
// or a brand-new one); the workspace is where that repo is checked out on a
// per-run branch. In docker-compose the Root is a shared named volume mounted in
// the coordinator and every agent.
package workspace

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const botName = "AI Software Factory"
const botEmail = "factory@ai-software-factory.local"

// Kind is how a run's repository was resolved.
type Kind string

const (
	KindNew    Kind = "new"    // brand-new local repo (git init)
	KindLocal  Kind = "local"  // linked worktree of an existing local repo
	KindRemote Kind = "remote" // clone of a remote repo
)

// RepoRef describes the repository a run targets.
type RepoRef struct {
	// URL is the raw repoURL string from the request: "" (new), a local path or
	// file:// URL (existing local), or an https/ssh/git URL (remote).
	URL        string
	BaseBranch string
}

// ParseRepoURL builds a RepoRef. baseBranch defaults to "main" when empty.
func ParseRepoURL(url, baseBranch string) RepoRef {
	if baseBranch == "" {
		baseBranch = "main"
	}
	return RepoRef{URL: strings.TrimSpace(url), BaseBranch: baseBranch}
}

// Kind classifies a RepoRef, inspecting the filesystem for local paths.
func (r RepoRef) Kind() Kind {
	if r.URL == "" {
		return KindNew
	}
	if p, ok := localPath(r.URL); ok {
		if isGitRepo(p) {
			return KindLocal
		}
		return KindNew // a local path that isn't a repo yet -> scaffold one
	}
	return KindRemote
}

// localPath returns the on-disk path for a local repoURL (bare path or file://),
// and false for remote URLs.
func localPath(url string) (string, bool) {
	if strings.HasPrefix(url, "file://") {
		return strings.TrimPrefix(url, "file://"), true
	}
	if strings.Contains(url, "://") || strings.Contains(url, "@") && strings.Contains(url, ":") {
		return "", false // scheme URL or scp-like remote
	}
	abs, err := filepath.Abs(url)
	if err != nil {
		return "", false
	}
	return abs, true
}

func isGitRepo(path string) bool {
	out, err := git(context.Background(), path, "rev-parse", "--git-dir")
	return err == nil && strings.TrimSpace(out) != ""
}

// Manager owns the workspace root and prepares per-run repos.
type Manager struct {
	Root string
}

// NewManager returns a Manager rooted at root (default: ./workspace).
func NewManager(root string) *Manager {
	if root == "" {
		root = "workspace"
	}
	return &Manager{Root: root}
}

// RepoDir is the on-disk path of a run's checkout.
func (m *Manager) RepoDir(runID string) string {
	return filepath.Join(m.Root, runID, "repo")
}

func branchName(runID string) string {
	if len(runID) > 8 {
		runID = runID[:8]
	}
	return "asf/run-" + runID
}

// Prepare resolves ref into a per-run workspace and returns a Repo on the run's
// branch. It replaces the Phase-2 Create.
func (m *Manager) Prepare(ctx context.Context, runID string, ref RepoRef) (*Repo, error) {
	dir := m.RepoDir(runID)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return nil, err
	}
	branch := branchName(runID)
	repo := &Repo{Dir: dir, BaseBranch: ref.BaseBranch, WorkBranch: branch}

	switch ref.Kind() {
	case KindNew:
		if err := runAll(ctx, dir, [][]string{
			{"init", "-q", "-b", ref.BaseBranch},
		}, true); err != nil {
			return nil, err
		}
		if err := repo.configIdentity(ctx); err != nil {
			return nil, err
		}
		if err := runAll(ctx, dir, [][]string{
			{"commit", "-q", "--allow-empty", "-m", "chore: empty base"},
			{"checkout", "-q", "-b", branch},
		}, false); err != nil {
			return nil, err
		}
		repo.Kind = KindNew

	case KindLocal:
		src, _ := localPath(ref.URL)
		src, _ = filepath.Abs(src)
		// A worktree keeps the branch in the user's own repo. Fall back to a
		// file:// clone if `worktree add` refuses (e.g. a bare source).
		if _, err := git(ctx, src, "worktree", "add", "-b", branch, dir, ref.BaseBranch); err != nil {
			if err := cloneInto(ctx, "file://"+src, dir, ref.BaseBranch, branch); err != nil {
				return nil, fmt.Errorf("workspace: local repo %s: worktree and clone both failed: %w", src, err)
			}
			repo.Kind = KindRemote
			repo.Remote = "file://" + src
		} else {
			repo.Kind = KindLocal
			repo.Source = src
		}
		if err := repo.configIdentity(ctx); err != nil {
			return nil, err
		}

	case KindRemote:
		if err := cloneInto(ctx, ref.URL, dir, ref.BaseBranch, branch); err != nil {
			return nil, err
		}
		repo.Kind = KindRemote
		repo.Remote = ref.URL
		if err := repo.configIdentity(ctx); err != nil {
			return nil, err
		}
	}
	return repo, nil
}

func cloneInto(ctx context.Context, url, dir, base, branch string) error {
	if out, err := git(ctx, ".", "clone", "-q", url, dir); err != nil {
		return fmt.Errorf("git clone: %v: %s", err, out)
	}
	// checkout base if it exists, then branch off it.
	_, _ = git(ctx, dir, "checkout", "-q", base)
	if out, err := git(ctx, dir, "checkout", "-q", "-b", branch); err != nil {
		return fmt.Errorf("git checkout -b: %v: %s", err, out)
	}
	return nil
}

func runAll(ctx context.Context, dir string, steps [][]string, mkdir bool) error {
	if mkdir {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	for _, args := range steps {
		if out, err := git(ctx, dir, args...); err != nil {
			return fmt.Errorf("workspace: git %s: %v: %s", args[0], err, out)
		}
	}
	return nil
}

// Remove tears down a run's workspace, unregistering a worktree if needed.
func (m *Manager) Remove(ctx context.Context, runID string, repo *Repo) error {
	if repo != nil && repo.Kind == KindLocal && repo.Source != "" {
		_, _ = git(ctx, repo.Source, "worktree", "remove", "--force", m.RepoDir(runID))
	}
	return os.RemoveAll(filepath.Join(m.Root, runID))
}

// Prune keeps the newest `keep` run directories and removes the rest. Worktree
// registrations for pruned runs are best-effort cleaned by callers that still
// hold the Repo; a bare `rm -rf` here can leave a stale registration, which
// `git worktree prune` in the source repo clears.
func (m *Manager) Prune(keep int) error {
	entries, err := os.ReadDir(m.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	type d struct {
		name string
		mod  time.Time
	}
	var dirs []d
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		dirs = append(dirs, d{e.Name(), info.ModTime()})
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].mod.After(dirs[j].mod) })
	for i := keep; i < len(dirs); i++ {
		_ = os.RemoveAll(filepath.Join(m.Root, dirs[i].name))
	}
	return nil
}

// Repo is a single per-run checkout on the run's branch.
type Repo struct {
	Dir        string
	Kind       Kind
	BaseBranch string
	WorkBranch string
	Remote     string // set for KindRemote (and the clone fallback)
	Source     string // set for KindLocal (abs path of the user's repo)
}

// Open returns a Repo for an existing workspace dir (used by agents on the shared
// volume). It reads the branch names from git.
func Open(ctx context.Context, dir string) (*Repo, error) {
	if _, err := git(ctx, dir, "rev-parse", "--is-inside-work-tree"); err != nil {
		return nil, fmt.Errorf("workspace: %s is not a git repo: %w", dir, err)
	}
	head, _ := git(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	r := &Repo{Dir: dir, WorkBranch: strings.TrimSpace(head), BaseBranch: "main"}
	return r, nil
}

func (r *Repo) configIdentity(ctx context.Context) error {
	for _, kv := range [][2]string{{"user.name", botName}, {"user.email", botEmail}, {"commit.gpgsign", "false"}} {
		if out, err := git(ctx, r.Dir, "config", kv[0], kv[1]); err != nil {
			return fmt.Errorf("git config %s: %v: %s", kv[0], err, out)
		}
	}
	return nil
}

// WriteFiles writes path->content under the repo, creating parent dirs. Absolute
// paths and any parent-dir traversal are rejected.
func (r *Repo) WriteFiles(files map[string]string) ([]string, error) {
	written := make([]string, 0, len(files))
	for p, content := range files {
		if filepath.IsAbs(p) || strings.Contains(p, "..") {
			continue
		}
		clean := filepath.Clean(p)
		if clean == "" || clean == "." {
			continue
		}
		full := filepath.Join(r.Dir, clean)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return written, err
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return written, err
		}
		written = append(written, filepath.ToSlash(clean))
	}
	sort.Strings(written)
	return written, nil
}

// Commit stages everything and commits. Returns the new HEAD sha, or ("", nil)
// when there is nothing to commit.
func (r *Repo) Commit(ctx context.Context, role, msg string) (string, error) {
	if out, err := git(ctx, r.Dir, "add", "-A"); err != nil {
		return "", fmt.Errorf("git add: %v: %s", err, out)
	}
	status, err := git(ctx, r.Dir, "status", "--porcelain")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(status) == "" {
		return "", nil
	}
	full := fmt.Sprintf("%s: %s", role, msg)
	if out, err := git(ctx, r.Dir, "commit", "-q", "-m", full); err != nil {
		return "", fmt.Errorf("git commit: %v: %s", err, out)
	}
	sha, err := git(ctx, r.Dir, "rev-parse", "HEAD")
	return strings.TrimSpace(sha), err
}

// mergeBase returns the common ancestor of the base branch and HEAD (falling
// back to the first commit when there is no base ref, e.g. a fresh repo).
func (r *Repo) mergeBase(ctx context.Context) string {
	base := r.BaseBranch
	if base == "" {
		base = "main"
	}
	for _, ref := range []string{base, "origin/" + base} {
		if out, err := git(ctx, r.Dir, "merge-base", ref, "HEAD"); err == nil {
			if s := strings.TrimSpace(out); s != "" {
				return s
			}
		}
	}
	out, _ := git(ctx, r.Dir, "rev-list", "--max-parents=0", "HEAD")
	return strings.TrimSpace(strings.Split(strings.TrimSpace(out), "\n")[0])
}

// HasCommits reports whether the work branch is ahead of the base.
func (r *Repo) HasCommits(ctx context.Context) bool {
	mb := r.mergeBase(ctx)
	if mb == "" {
		return false
	}
	out, err := git(ctx, r.Dir, "rev-list", "--count", mb+"..HEAD")
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) != "" && strings.TrimSpace(out) != "0"
}

// Diff returns the unified diff of the run's work against the base branch.
func (r *Repo) Diff(ctx context.Context) (string, error) {
	mb := r.mergeBase(ctx)
	if mb == "" {
		return "", nil
	}
	return git(ctx, r.Dir, "diff", mb)
}

// ChangedFiles lists files changed against the base branch.
func (r *Repo) ChangedFiles(ctx context.Context) ([]string, error) {
	mb := r.mergeBase(ctx)
	if mb == "" {
		return nil, nil
	}
	out, err := git(ctx, r.Dir, "diff", "--name-only", mb)
	if err != nil {
		return nil, err
	}
	return nonEmptyLines(out), nil
}

// Tree lists tracked files (for LLM context).
func (r *Repo) Tree(ctx context.Context) ([]string, error) {
	out, err := git(ctx, r.Dir, "ls-files")
	if err != nil {
		return nil, err
	}
	return nonEmptyLines(out), nil
}

// Summary is a compact repo snapshot for grounding the planner.
type Summary struct {
	Kind       Kind     `json:"kind"`
	Tree       []string `json:"tree"`
	ReadmeHead string   `json:"readmeHead,omitempty"`
}

// Summarize returns a capped tree listing and the head of any README.
func (r *Repo) Summarize(ctx context.Context, maxFiles int) Summary {
	s := Summary{Kind: r.Kind}
	tree, _ := r.Tree(ctx)
	if len(tree) > maxFiles {
		s.Tree = append(tree[:maxFiles:maxFiles], fmt.Sprintf("… (%d more)", len(tree)-maxFiles))
	} else {
		s.Tree = tree
	}
	for _, name := range []string{"README.md", "README", "readme.md"} {
		if b, err := os.ReadFile(filepath.Join(r.Dir, name)); err == nil {
			head := string(b)
			if len(head) > 2000 {
				head = head[:2000] + "\n…"
			}
			s.ReadmeHead = head
			break
		}
	}
	return s
}

// Push pushes the work branch to origin. No-op unless the run cloned a remote.
func (r *Repo) Push(ctx context.Context) error {
	if r.Kind != KindRemote || r.Remote == "" {
		return nil
	}
	if out, err := git(ctx, r.Dir, "push", "-u", "origin", r.WorkBranch); err != nil {
		return fmt.Errorf("git push: %v: %s", err, out)
	}
	return nil
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	full := args
	if dir != "" && dir != "." {
		full = append([]string{"-C", dir}, args...)
	}
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	return string(out), err
}
