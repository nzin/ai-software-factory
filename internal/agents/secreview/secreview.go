// Package secreview is the executor for the security-reviewer agent: it runs
// SAST tools over the per-run worktree and an LLM pass over the diff, then merges
// the findings.
package secreview

import (
	"context"
	"fmt"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/nzin/ai-software-factory/internal/agentkit"
	"github.com/nzin/ai-software-factory/internal/factory"
	"github.com/nzin/ai-software-factory/internal/llm"
	"github.com/nzin/ai-software-factory/internal/sast"
	"github.com/nzin/ai-software-factory/internal/workspace"
)

const (
	maxDiffBytes = 80_000
	maxPRDBytes  = 12_000
)

func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n… (truncated)"
}

const outputContract = `
After your analysis, output ONLY a JSON array of findings:

[
  { "severity": "high", "category": "injection", "file": "path", "line": 12,
    "title": "short description", "suggestion": "how to fix",
    "targetRole": "backend" }
]

severity is one of: critical, high, medium, low, info.
targetRole is the developer who should fix it: backend, frontend, or mobile.
Empty array if nothing found.`

// Executor builds the security-reviewer executor.
func Executor(client *llm.Client, systemPrompt string) a2asrv.AgentExecutor {
	return agentkit.DispatchExecutor(func(ctx context.Context, env factory.DispatchEnvelope) (factory.ResultEnvelope, error) {
		repo, err := workspace.Open(ctx, env.WorkspaceDir)
		if err != nil {
			return factory.ResultEnvelope{}, err
		}
		repo.BaseBranch = env.BaseBranch

		toolFindings := sast.All(ctx, repo.Dir)

		diff, _ := repo.Diff(ctx)
		if len(diff) > maxDiffBytes {
			diff = diff[:maxDiffBytes] + "\n… (diff truncated)"
		}

		// The PRD is what makes severity judgeable: without it the reviewer
		// rates explicitly out-of-scope requirements (auth, persistence, TLS) as
		// high, which bounces the run back to a developer who cannot satisfy
		// them without contradicting the PRD.
		var b strings.Builder
		if env.PRDText != "" {
			fmt.Fprintf(&b, "# What was asked for (PRD)\n\n%s\n\n", head(env.PRDText, maxPRDBytes))
		}
		fmt.Fprintf(&b, "# Static analysis findings\n\n%s\n\n", renderFindings(toolFindings))
		fmt.Fprintf(&b, "# Diff to review\n\n```diff\n%s\n```\n%s", diff, outputContract)
		user := b.String()

		out, err := client.Complete(ctx, systemPrompt, user)
		if err != nil {
			return factory.ResultEnvelope{}, err
		}
		llmFindings := factory.ParseFindings(out)
		for i := range llmFindings {
			if llmFindings[i].Source == "" {
				llmFindings[i].Source = "security-reviewer"
			}
		}

		all := append(toolFindings, llmFindings...)
		return factory.ResultEnvelope{
			Role:       env.Stage,
			Summary:    fmt.Sprintf("%d SAST + %d review findings", len(toolFindings), len(llmFindings)),
			Findings:   all,
			Verdict:    factory.VerdictFor(all),
			TargetRole: factory.RouteRole(all, ""),
		}, nil
	})
}

func renderFindings(fs []factory.Finding) string {
	if len(fs) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for _, f := range fs {
		fmt.Fprintf(&b, "- [%s] %s:%d (%s) %s\n", f.Severity, f.File, f.Line, f.Source, f.Title)
	}
	return b.String()
}
