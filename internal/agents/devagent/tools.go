// Tools available to an agentic completion (see devagent.go's
// completeFilesAgentic): read_file/git_log/git_diff/grep are read-only and
// sandboxed to the run's own workspace directory, letting the model check a
// finding — or its own plan — against the actual current state of the repo
// and its prior attempts, instead of only trusting a finding's
// title/suggestion or the flattened current-file snapshot every prompt
// already carries. write_file/edit_file write to that same sandboxed
// directory only — no network, no execution. run_build/run_tests are the one
// deliberate exception to "no execution": they run a fixed, narrow set of
// build/test commands (go build/test, npm install/build/test) scoped to the
// workspace, not arbitrary shell.
package devagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nzin/ai-software-factory/internal/buildgate"
	"github.com/nzin/ai-software-factory/internal/llm"
	"github.com/nzin/ai-software-factory/internal/workspace"
)

// toolTimeout bounds one read-only tool invocation (a git or grep subprocess).
const toolTimeout = 20 * time.Second

// buildToolTimeout bounds one run_build/run_tests invocation — a real build
// or test run needs minutes, not seconds.
const buildToolTimeout = 5 * time.Minute

// toolState accumulates the repo-relative paths write_file/edit_file actually
// touched during one agentic loop, so the caller can commit exactly what was
// written once the loop finishes instead of parsing a final text blob.
// CompleteAgentic drives tool calls sequentially in a single goroutine, so no
// locking is needed.
type toolState struct {
	written map[string]bool
}

func newToolState() *toolState { return &toolState{written: map[string]bool{}} }

func (s *toolState) mark(paths []string) {
	for _, p := range paths {
		s.written[p] = true
	}
}

// touched returns the sorted, deduped set of paths marked so far.
func (s *toolState) touched() []string {
	out := make([]string, 0, len(s.written))
	for p := range s.written {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// devTools is the tool set offered to an agentic completion.
func devTools() []llm.Tool {
	return []llm.Tool{
		{
			Name: "read_file",
			Description: "Read one text file from the repository, given a path relative to the " +
				"repo root (as it appears in the current-repository file list already in your " +
				"prompt). Useful for a file the snapshot omitted for length, or for " +
				"build-gate-raw/<kind>.log — the raw, unsummarized command output a finding's " +
				"title/suggestion were derived from, not otherwise shown to you.",
			Properties: map[string]any{
				"path": map[string]any{"type": "string", "description": "repo-relative path"},
			},
			Required: []string{"path"},
		},
		{
			Name: "git_log",
			Description: "List this run's commit history (newest first) that touched one path. " +
				"Use it to check whether a previous fix-pass attempt already touched the file a " +
				"finding names, and what it did there, before deciding this attempt's change.",
			Properties: map[string]any{
				"path":  map[string]any{"type": "string", "description": "repo-relative path"},
				"limit": map[string]any{"type": "integer", "description": "max commits to return (default 10)"},
			},
			Required: []string{"path"},
		},
		{
			Name: "git_diff",
			Description: "Show the cumulative diff of one path against the base branch — " +
				"everything this run has changed there so far, across every attempt. Use it " +
				"alongside git_log to see exactly what a previous attempt changed, not just that " +
				"it changed something.",
			Properties: map[string]any{
				"path": map[string]any{"type": "string", "description": "repo-relative path"},
			},
			Required: []string{"path"},
		},
		{
			Name: "grep",
			Description: "Search the repository for a plain-text or extended-regexp pattern; " +
				"returns matching lines as file:line:text. Use it to find where a symbol, " +
				"string, or interaction is actually implemented — e.g. to check a finding's " +
				"claim against what the code really does, or to find related code the current-" +
				"repository snapshot didn't have room to include.",
			Properties: map[string]any{
				"pattern": map[string]any{"type": "string"},
				"path":    map[string]any{"type": "string", "description": "optional: limit the search to this file or directory (repo-relative)"},
			},
			Required: []string{"pattern"},
		},
		{
			Name: "write_file",
			Description: "Create a new file or overwrite an existing file's entire contents, at a " +
				"path relative to the repo root. Creates parent directories as needed. Use this for " +
				"a brand-new file, or when replacing a file wholesale is simpler than editing it. " +
				"For a small, targeted change to an existing file, prefer edit_file — it fails " +
				"loudly instead of silently clobbering unrelated content.",
			Properties: map[string]any{
				"path":    map[string]any{"type": "string", "description": "repo-relative path to write"},
				"content": map[string]any{"type": "string", "description": "full file contents to write, verbatim"},
			},
			Required: []string{"path", "content"},
		},
		{
			Name: "edit_file",
			Description: "Replace an exact, unique occurrence of old_string with new_string in an " +
				"existing file (path relative to the repo root). old_string must match the file's " +
				"current content exactly, including whitespace, and must be unique in the file " +
				"unless replace_all is true. Fails if old_string is not found, if it matches more " +
				"than once without replace_all, or if the file doesn't exist yet (use write_file " +
				"for a new file).",
			Properties: map[string]any{
				"path":        map[string]any{"type": "string", "description": "repo-relative path to edit"},
				"old_string":  map[string]any{"type": "string", "description": "exact text to find; must be unique unless replace_all is set"},
				"new_string":  map[string]any{"type": "string", "description": "text to replace it with"},
				"replace_all": map[string]any{"type": "boolean", "description": "replace every occurrence instead of requiring exactly one match (default false)"},
			},
			Required: []string{"path", "old_string", "new_string"},
		},
		{
			Name: "run_build",
			Description: "Build the project so far: `go build ./...` for a Go module, `npm install` " +
				"+ `npm run build` (if a build script exists) for an npm package. Detects the " +
				"project automatically. Can take several minutes. Use it to verify a change " +
				"compiles before finishing, especially after several edits.",
			Properties: map[string]any{
				"path": map[string]any{"type": "string", "description": "optional: limit to the Go module or npm package rooted at this repo-relative directory; omit to check everything detected in the repo"},
			},
			Required: []string{},
		},
		{
			Name: "run_tests",
			Description: "Run the project's tests: `go test ./...` for a Go module, `npm install` + " +
				"`npm test` for an npm package (skipped if there is no real test script). Detects " +
				"the project automatically. Can take several minutes.",
			Properties: map[string]any{
				"path": map[string]any{"type": "string", "description": "optional: limit to the Go module or npm package rooted at this repo-relative directory; omit to check everything detected in the repo"},
			},
			Required: []string{},
		},
	}
}

// toolExecFunc dispatches one devTools call, sandboxed to repo.Dir.
func toolExecFunc(repo *workspace.Repo, state *toolState) llm.ToolExecFunc {
	return func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		switch name {
		case "read_file":
			var args struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return "", fmt.Errorf("bad input: %w", err)
			}
			return toolReadFile(repo.Dir, args.Path)

		case "git_log":
			var args struct {
				Path  string `json:"path"`
				Limit int    `json:"limit"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return "", fmt.Errorf("bad input: %w", err)
			}
			out, err := repo.LogPath(ctx, args.Path, args.Limit)
			if err != nil {
				return "", err
			}
			if out == "" {
				return "(no commits touch this path)", nil
			}
			return out, nil

		case "git_diff":
			var args struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return "", fmt.Errorf("bad input: %w", err)
			}
			out, err := repo.DiffPath(ctx, args.Path)
			if err != nil {
				return "", err
			}
			if out == "" {
				return "(no diff for this path against the base branch)", nil
			}
			return out, nil

		case "grep":
			var args struct {
				Pattern string `json:"pattern"`
				Path    string `json:"path"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return "", fmt.Errorf("bad input: %w", err)
			}
			return toolGrep(ctx, repo.Dir, args.Pattern, args.Path)

		case "write_file":
			var args struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return "", fmt.Errorf("bad input: %w", err)
			}
			return toolWriteFile(repo, state, args.Path, args.Content)

		case "edit_file":
			var args struct {
				Path       string `json:"path"`
				OldString  string `json:"old_string"`
				NewString  string `json:"new_string"`
				ReplaceAll bool   `json:"replace_all"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return "", fmt.Errorf("bad input: %w", err)
			}
			return toolEditFile(repo, state, args.Path, args.OldString, args.NewString, args.ReplaceAll)

		case "run_build":
			var args struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return "", fmt.Errorf("bad input: %w", err)
			}
			return toolRunBuild(ctx, repo.Dir, args.Path)

		case "run_tests":
			var args struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return "", fmt.Errorf("bad input: %w", err)
			}
			return toolRunTests(ctx, repo.Dir, args.Path)

		default:
			return "", fmt.Errorf("unknown tool %q", name)
		}
	}
}

// resolveInRepo resolves a repo-relative path and rejects anything that would
// escape repoDir (traversal, or an absolute path elsewhere).
func resolveInRepo(repoDir, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("path is required")
	}
	full := filepath.Join(repoDir, rel)
	root := filepath.Clean(repoDir) + string(filepath.Separator)
	if !strings.HasPrefix(filepath.Clean(full)+string(filepath.Separator), root) {
		return "", fmt.Errorf("path %q escapes the repository", rel)
	}
	return full, nil
}

func toolReadFile(repoDir, rel string) (string, error) {
	full, err := resolveInRepo(repoDir, rel)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", rel, err)
	}
	return string(data), nil
}

// toolGrep runs a bounded, read-only `git grep` over the workspace (or
// repoDir/path when given) for pattern.
func toolGrep(ctx context.Context, repoDir, pattern, path string) (string, error) {
	if pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	if strings.Contains(path, "..") {
		return "", fmt.Errorf("path %q must not contain ..", path)
	}
	pathspec := "."
	if path != "" {
		pathspec = path
	}
	cctx, cancel := context.WithTimeout(ctx, toolTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", "-C", repoDir,
		"grep", "-n", "-I", "--extended-regexp", "--max-count=50", "-e", pattern, "--", pathspec)
	out, err := cmd.CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		// git grep exits 1 for "no matches" — not a real error.
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "(no matches)", nil
		}
		return "", fmt.Errorf("grep: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if strings.TrimSpace(string(out)) == "" {
		return "(no matches)", nil
	}
	return string(out), nil
}

// toolWriteFile creates or overwrites rel with content, sandboxed to
// repo.Dir, and records it in state.
func toolWriteFile(repo *workspace.Repo, state *toolState, rel, content string) (string, error) {
	if _, err := resolveInRepo(repo.Dir, rel); err != nil {
		return "", err
	}
	written, err := repo.WriteFiles(map[string]string{rel: content})
	if err != nil {
		return "", fmt.Errorf("write %s: %w", rel, err)
	}
	if len(written) == 0 {
		// resolveInRepo accepted rel but WriteFiles' own (stricter) path
		// validation silently rejected it — surface that as an error instead
		// of a false "success".
		return "", fmt.Errorf("write_file: %q was rejected (must be a repo-relative path with no \"..\")", rel)
	}
	state.mark(written)
	return fmt.Sprintf("wrote %d bytes to %s", len(content), written[0]), nil
}

// toolEditFile replaces an exact occurrence of oldStr with newStr in rel,
// sandboxed to repo.Dir, and records the result in state.
func toolEditFile(repo *workspace.Repo, state *toolState, rel, oldStr, newStr string, replaceAll bool) (string, error) {
	full, err := resolveInRepo(repo.Dir, rel)
	if err != nil {
		return "", err
	}
	if oldStr == newStr {
		return "", fmt.Errorf("edit_file: old_string and new_string are identical (no-op) for %s", rel)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("edit_file: %s does not exist (use write_file to create it)", rel)
		}
		return "", fmt.Errorf("edit_file: read %s: %w", rel, err)
	}
	content := string(data)
	n := strings.Count(content, oldStr)
	if n == 0 {
		return "", fmt.Errorf("edit_file: old_string not found in %s", rel)
	}
	if n > 1 && !replaceAll {
		return "", fmt.Errorf("edit_file: old_string matches %d times in %s — include more surrounding context to make it unique, or set replace_all", n, rel)
	}
	var updated string
	if replaceAll {
		updated = strings.ReplaceAll(content, oldStr, newStr)
	} else {
		updated = strings.Replace(content, oldStr, newStr, 1)
	}
	written, err := repo.WriteFiles(map[string]string{rel: updated})
	if err != nil {
		return "", fmt.Errorf("edit_file: write %s: %w", rel, err)
	}
	if len(written) == 0 {
		return "", fmt.Errorf("edit_file: %q was rejected (must be a repo-relative path with no \"..\")", rel)
	}
	state.mark(written)
	if replaceAll {
		return fmt.Sprintf("replaced %d occurrence(s) in %s", n, written[0]), nil
	}
	return fmt.Sprintf("replaced 1 occurrence in %s", written[0]), nil
}

// scopedDir resolves an optional repo-relative directory for run_build/
// run_tests; "" (the common case) means the whole workspace.
func scopedDir(repoDir, rel string) (string, error) {
	if rel == "" {
		return repoDir, nil
	}
	return resolveInRepo(repoDir, rel)
}

func toolRunBuild(ctx context.Context, repoDir, rel string) (string, error) {
	dir, err := scopedDir(repoDir, rel)
	if err != nil {
		return "", err
	}
	cctx, cancel := context.WithTimeout(ctx, buildToolTimeout)
	defer cancel()
	out, ok := buildgate.RunBuild(cctx, dir)
	if !ok {
		return "", fmt.Errorf("build failed:\n%s", out)
	}
	return out, nil
}

func toolRunTests(ctx context.Context, repoDir, rel string) (string, error) {
	dir, err := scopedDir(repoDir, rel)
	if err != nil {
		return "", err
	}
	cctx, cancel := context.WithTimeout(ctx, buildToolTimeout)
	defer cancel()
	out, ok := buildgate.RunTests(cctx, dir)
	if !ok {
		return "", fmt.Errorf("tests failed:\n%s", out)
	}
	return out, nil
}
