// Package buildgate is the executor for the build-gate agent: it compiles,
// tests, validates the docker-compose deployment and runs the component test
// suite over the per-run workspace, then makes one best-effort LLM pass to turn
// raw failure output into the fewest, clearest, correctly-routed findings.
package buildgate

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/nzin/ai-software-factory/internal/agentkit"
	bg "github.com/nzin/ai-software-factory/internal/buildgate"
	"github.com/nzin/ai-software-factory/internal/factory"
	"github.com/nzin/ai-software-factory/internal/llm"
	"github.com/nzin/ai-software-factory/internal/testharness"
	"github.com/nzin/ai-software-factory/internal/workspace"
)

var rawFailureKindRe = regexp.MustCompile(`[^a-z0-9]+`)

const maxDiffBytes = 80_000

const outputContract = `
Output ONLY a JSON array of findings — the smallest set a developer can act on:

[
  { "severity": "high", "category": "compile", "file": "path", "line": 12,
    "title": "the one-line root cause", "suggestion": "the concrete fix",
    "evidence": "the verbatim lines from the output below that this finding is
    based on — the actual error/assertion/call-log text, not your paraphrase of
    it", "targetRole": "backend" }
]

Rules:
- Collapse a cascade of errors from one root cause into ONE finding at the
  definition site. Do not emit a finding per downstream error.
- Keep the real file:line from the output. Never invent a file or line.
- evidence is mandatory and must be copied verbatim from the raw output you were
  given — never invent or paraphrase it there. Before writing title/suggestion,
  re-read the evidence you are about to quote and check your title actually
  matches what it says (e.g. what triggered it, and at what point in the test it
  happened) — do not describe a scenario the evidence doesn't support.
- severity is one of: critical, high, medium, low, info. Use high for anything
  that breaks the build, the deploy, or a component test.
- targetRole is backend, frontend, or mobile — whichever owns the failing code.
- If the output already describes a single clean problem, pass it through.
- Empty array only if nothing in the output is actually broken.`

// Executor builds the build-gate executor.
func Executor(client *llm.Client, systemPrompt string) a2asrv.AgentExecutor {
	return agentkit.DispatchExecutor(func(ctx context.Context, env factory.DispatchEnvelope) (factory.ResultEnvelope, error) {
		repo, err := workspace.Open(ctx, env.WorkspaceDir)
		if err != nil {
			return factory.ResultEnvelope{}, err
		}
		repo.BaseBranch = env.BaseBranch

		commitSHA, filesWritten := refreshHarness(ctx, repo, env.Stage)

		changed, _ := repo.ChangedFiles(ctx)
		res := bg.Check(ctx, env.RunID, repo.Dir, changed)
		findings := res.Findings

		if client != nil && len(res.Failures) > 0 {
			diff, _ := repo.Diff(ctx)
			if tight := tighten(ctx, client, systemPrompt, diff, res); len(tight) > 0 {
				findings = tight
			}
		}
		for i := range findings {
			if findings[i].Source == "" {
				findings[i].Source = "build-gate"
			}
		}

		if len(res.Screenshots) > 0 {
			if err := repo.AddPaths(ctx, res.Screenshots); err == nil {
				if sha, err := repo.Commit(ctx, env.Stage, "attach e2e screenshots"); err == nil && sha != "" {
					commitSHA = sha
					filesWritten = append(filesWritten, res.Screenshots...)
				}
			}
		}

		if rawPaths, err := writeRawFailures(repo.Dir, res.Failures); err == nil && len(rawPaths) > 0 {
			if err := repo.AddPaths(ctx, rawPaths); err == nil {
				if sha, err := repo.Commit(ctx, env.Stage, "attach raw failure logs"); err == nil && sha != "" {
					commitSHA = sha
					filesWritten = append(filesWritten, rawPaths...)
				}
			}
		}

		return factory.ResultEnvelope{
			Role:         env.Stage,
			Summary:      fmt.Sprintf("%s (%d findings)", res.Summary, len(findings)),
			Findings:     findings,
			Verdict:      factory.VerdictFor(findings),
			TargetRole:   factory.RouteRole(findings, ""),
			CommitSHA:    commitSHA,
			FilesWritten: filesWritten,
		}, nil
	})
}

// writeRawFailures persists each failed command's full (already-clipped) raw
// output to <dir>/build-gate-raw/<kind>.log, one stable path per kind so repeat
// runs overwrite rather than accumulate. This is the text the LLM "tighten"
// pass summarized into findings — kept around so a wrong or incomplete
// summary can still be checked against what actually happened, by a human
// resuming a stuck run or by a tool-using developer fix pass.
func writeRawFailures(dir string, failures []bg.Failure) ([]string, error) {
	if len(failures) == 0 {
		return nil, nil
	}
	rawDir := filepath.Join(dir, factory.RawFailuresDir)
	if err := os.MkdirAll(rawDir, 0o755); err != nil {
		return nil, err
	}
	var written []string
	for _, f := range failures {
		slug := strings.Trim(rawFailureKindRe.ReplaceAllString(strings.ToLower(f.Kind), "-"), "-")
		if slug == "" {
			slug = "failure"
		}
		rel := filepath.Join(factory.RawFailuresDir, slug+".log")
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(f.Output), 0o644); err != nil {
			return written, err
		}
		written = append(written, filepath.ToSlash(rel))
	}
	return written, nil
}

// refreshHarness re-lays the factory-owned test harness before the checks run,
// so a developer fix pass that edited one of its files can't break the
// component run, and commits the refresh so later fix passes see it. It is
// best-effort: on any error the gate still checks what is there.
func refreshHarness(ctx context.Context, repo *workspace.Repo, stage string) (sha string, written []string) {
	written, removed, err := testharness.Materialize(repo.Dir)
	if err != nil {
		log.Printf("build-gate: refresh test harness: %v", err)
		return "", nil
	}
	if len(written)+len(removed) == 0 {
		return "", nil
	}
	if err := repo.AddPaths(ctx, written); err != nil {
		log.Printf("build-gate: stage test harness: %v", err)
		return "", nil
	}
	if sha, err = repo.Commit(ctx, stage, "refresh factory-owned test harness"); err != nil {
		log.Printf("build-gate: commit test harness: %v", err)
		return "", nil
	}
	return sha, written
}

// completer is the slice of *llm.Client tighten needs (so it can be stubbed).
type completer interface {
	Complete(ctx context.Context, system, user string) (string, error)
}

// tighten asks the model to reduce raw failure output to a minimal, routed
// finding set. It is best-effort: any error returns nil and the caller keeps the
// deterministic findings.
func tighten(ctx context.Context, client completer, systemPrompt, diff string, res bg.Result) []factory.Finding {
	var b strings.Builder
	if diff != "" {
		if len(diff) > maxDiffBytes {
			diff = diff[:maxDiffBytes] + "\n… (diff truncated)"
		}
		fmt.Fprintf(&b, "# The change under test\n\n```diff\n%s\n```\n\n", diff)
	}
	for _, f := range res.Failures {
		fmt.Fprintf(&b, "# `%s` failed\n\n```\n%s\n```\n\n", f.Kind, f.Output)
	}
	fmt.Fprintf(&b, "# Findings parsed so far (anchors — keep the file:line)\n\n%s\n%s",
		renderFindings(res.Findings), outputContract)

	out, err := client.Complete(ctx, systemPrompt, b.String())
	if err != nil {
		return nil
	}
	tight := factory.ParseFindings(out)
	for i := range tight {
		tight[i].Source = "build-gate"
	}
	return tight
}

func renderFindings(fs []factory.Finding) string {
	if len(fs) == 0 {
		return "(none parsed — derive the findings from the failure output above)"
	}
	var b strings.Builder
	for _, f := range fs {
		fmt.Fprintf(&b, "- [%s] %s:%d — %s\n", f.Severity, f.File, f.Line, f.Title)
	}
	return b.String()
}
