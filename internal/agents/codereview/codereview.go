// Package codereview is the executor for the code-reviewer agent: it runs the
// Kodus CLI over the per-run worktree and produces a short human-readable
// summary of the findings.
package codereview

import (
	"context"
	"fmt"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/nzin/ai-software-factory/internal/agentkit"
	"github.com/nzin/ai-software-factory/internal/factory"
	"github.com/nzin/ai-software-factory/internal/kodus"
	"github.com/nzin/ai-software-factory/internal/llm"
	"github.com/nzin/ai-software-factory/internal/workspace"
)

// Executor builds the code-reviewer executor. systemPrompt is used only to
// summarise the Kodus findings.
func Executor(client *llm.Client, systemPrompt string) a2asrv.AgentExecutor {
	return agentkit.DispatchExecutor(func(ctx context.Context, env factory.DispatchEnvelope) (factory.ResultEnvelope, error) {
		repo, err := workspace.Open(ctx, env.WorkspaceDir)
		if err != nil {
			return factory.ResultEnvelope{}, err
		}
		base := env.BaseBranch
		if base == "" {
			base = repo.BaseBranch
		}

		findings, err := kodus.Review(ctx, repo.Dir, base)
		if err != nil {
			return factory.ResultEnvelope{}, err
		}

		summary := renderCounts(findings)
		if client != nil && len(findings) > 0 {
			if s, err := client.Complete(ctx, systemPrompt, "# Kodus findings\n\n"+renderFindings(findings)); err == nil {
				summary = s
			}
		}
		// Kodus findings rarely carry a developer attribution; when none do,
		// leave TargetRole empty so the coordinator routes by last developer.
		target := ""
		for _, f := range findings {
			if factory.IsDeveloperRole(f.TargetRole) {
				target = factory.RouteRole(findings, "")
				break
			}
		}
		return factory.ResultEnvelope{
			Role:       env.Stage,
			Summary:    summary,
			Findings:   findings,
			Verdict:    factory.VerdictFor(findings),
			TargetRole: target,
		}, nil
	})
}

func renderFindings(fs []factory.Finding) string {
	var b strings.Builder
	for _, f := range fs {
		fmt.Fprintf(&b, "- [%s] %s:%d — %s\n", f.Severity, f.File, f.Line, f.Title)
		if f.Suggestion != "" {
			fmt.Fprintf(&b, "  suggestion: %s\n", f.Suggestion)
		}
	}
	return b.String()
}

func renderCounts(fs []factory.Finding) string {
	by := map[string]int{}
	for _, f := range fs {
		by[f.Severity]++
	}
	parts := make([]string, 0, len(by))
	for _, sev := range []string{"critical", "high", "medium", "low", "info"} {
		if by[sev] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", by[sev], sev))
		}
	}
	if len(parts) == 0 {
		return "Kodus review: no findings"
	}
	return "Kodus review: " + strings.Join(parts, ", ")
}
