// Tools available to a fix-pass completion (see devagent.go's
// completeFilesAgentic): a small, read-only, sandboxed set that lets the model
// check a finding against the actual current state of the repo and its own
// prior attempts, instead of only trusting the finding's title/suggestion and
// the flattened current-file snapshot every prompt already carries. No tool
// here writes, executes arbitrary commands, or reaches the network — each one
// is scoped to the run's own workspace directory.
package devagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nzin/ai-software-factory/internal/llm"
	"github.com/nzin/ai-software-factory/internal/workspace"
)

// toolTimeout bounds one tool invocation (a git or grep subprocess).
const toolTimeout = 20 * time.Second

// fixPassTools is the tool set offered to a fix-pass completion.
func fixPassTools() []llm.Tool {
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
	}
}

// toolExecFunc dispatches one fixPassTools call, sandboxed to repo.Dir.
func toolExecFunc(repo *workspace.Repo) llm.ToolExecFunc {
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
