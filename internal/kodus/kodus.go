// Package kodus wraps the Kodus CLI (`kodus review`) so the code-reviewer agent
// can get structured findings for a local git diff. It targets a self-hosted
// Kodus API via KODUS_API_URL and authenticates with KODUS_TEAM_KEY.
package kodus

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/nzin/ai-software-factory/internal/factory"
)

// Review runs `kodus review` against the repo at dir, comparing to baseBranch,
// and returns structured findings.
//
// It never returns a hard error for a missing/unconfigured Kodus: in that case
// it returns a single informational Finding so the pipeline still completes.
func Review(ctx context.Context, dir, baseBranch string) ([]factory.Finding, error) {
	if _, err := exec.LookPath("kodus"); err != nil {
		return []factory.Finding{{
			Source: "kodus", Severity: "info", Title: "Kodus CLI not installed; code review skipped",
		}}, nil
	}
	if os.Getenv("KODUS_TEAM_KEY") == "" {
		return []factory.Finding{{
			Source: "kodus", Severity: "info",
			Title: "KODUS_TEAM_KEY not set; run scripts/kodus-setup.md to configure the self-hosted Kodus",
		}}, nil
	}

	// No --no-fast: the flag was removed upstream (current CLI already does a
	// full, non-fast review by default; --fast opts into the lighter mode).
	// Passing it makes the CLI exit with "Unknown option" before ever reaching
	// Kodus, so Review() would silently degrade to the unparseable-JSON path
	// on every real call.
	args := []string{"review", "--agent", "--format", "json"}
	if baseBranch != "" {
		args = append(args, "--branch", baseBranch)
	}
	cmd := exec.CommandContext(ctx, "kodus", args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()

	findings, perr := parse(out)
	if perr != nil {
		// Surface the raw error rather than failing the run.
		msg := strings.TrimSpace(string(out))
		if err != nil {
			msg = fmt.Sprintf("%v: %s", err, msg)
		}
		if len(msg) > 500 {
			msg = msg[:500] + "…"
		}
		return []factory.Finding{{
			Source: "kodus", Severity: "info",
			Title: "Kodus review did not return parseable JSON: " + msg,
		}}, nil
	}
	return findings, nil
}

// envelope is the Kodus --agent output shape.
type envelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type kodusIssue struct {
	File       string `json:"file"`
	Path       string `json:"path"`
	Line       int    `json:"line"`
	StartLine  int    `json:"startLine"`
	Severity   string `json:"severity"`
	Category   string `json:"category"`
	Label      string `json:"label"`
	Title      string `json:"title"`
	Message    string `json:"message"`
	Summary    string `json:"summary"`
	Suggestion string `json:"suggestion"`
}

func parse(out []byte) ([]factory.Finding, error) {
	// The CLI may print progress lines before the JSON; take the last {...} blob.
	blob := lastJSONObject(out)
	if blob == nil {
		return nil, fmt.Errorf("no JSON object in output")
	}
	var env envelope
	if err := json.Unmarshal(blob, &env); err != nil {
		return nil, err
	}
	if env.Error != nil && env.Error.Message != "" {
		return []factory.Finding{{Source: "kodus", Severity: "info", Title: "Kodus: " + env.Error.Message}}, nil
	}

	// data may be {issues:[...]} or {findings:[...]} or a bare array.
	var wrap struct {
		Issues   []kodusIssue `json:"issues"`
		Findings []kodusIssue `json:"findings"`
		Summary  string       `json:"summary"`
	}
	_ = json.Unmarshal(env.Data, &wrap)
	issues := wrap.Issues
	if len(issues) == 0 {
		issues = wrap.Findings
	}
	if len(issues) == 0 {
		var bare []kodusIssue
		if json.Unmarshal(env.Data, &bare) == nil {
			issues = bare
		}
	}

	findings := make([]factory.Finding, 0, len(issues)+1)
	for _, is := range issues {
		findings = append(findings, factory.Finding{
			Source:     "kodus",
			Severity:   strings.ToLower(firstNonEmpty(is.Severity, "info")),
			Category:   firstNonEmpty(is.Category, is.Label),
			File:       firstNonEmpty(is.File, is.Path),
			Line:       nonZero(is.Line, is.StartLine),
			Title:      firstNonEmpty(is.Title, is.Message, is.Summary, "Kodus finding"),
			Suggestion: is.Suggestion,
		})
	}
	if len(findings) == 0 {
		findings = append(findings, factory.Finding{Source: "kodus", Severity: "info", Title: "Kodus review: no issues found"})
	}
	return findings, nil
}

func lastJSONObject(b []byte) []byte {
	depth, start := 0, -1
	var best []byte
	for i, c := range b {
		switch c {
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if depth > 0 {
				depth--
				if depth == 0 && start >= 0 {
					best = b[start : i+1]
				}
			}
		}
	}
	return best
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func nonZero(vs ...int) int {
	for _, v := range vs {
		if v != 0 {
			return v
		}
	}
	return 0
}
