// Package buildgate is the executor for the build-gate agent: it compiles,
// tests, validates the docker-compose deployment and runs the component test
// suite over the per-run workspace, then makes one best-effort LLM pass to turn
// raw failure output into the fewest, clearest, correctly-routed findings.
package buildgate

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/nzin/ai-software-factory/internal/agentkit"
	bg "github.com/nzin/ai-software-factory/internal/buildgate"
	"github.com/nzin/ai-software-factory/internal/factory"
	"github.com/nzin/ai-software-factory/internal/llm"
	"github.com/nzin/ai-software-factory/internal/testharness"
	"github.com/nzin/ai-software-factory/internal/workspace"
)

const maxDiffBytes = 80_000

const outputContract = `
Output ONLY a JSON array of findings — the smallest set a developer can act on:

[
  { "severity": "high", "category": "compile", "file": "path", "line": 12,
    "title": "the one-line root cause", "suggestion": "the concrete fix",
    "targetRole": "backend" }
]

Rules:
- Collapse a cascade of errors from one root cause into ONE finding at the
  definition site. Do not emit a finding per downstream error.
- Keep the real file:line from the output. Never invent a file or line.
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
