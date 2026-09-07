// Package buildgate is the executor for the build-gate agent: it compiles and
// tests the per-run workspace and returns request_changes routed to the
// responsible developer when anything fails to build. It uses no LLM.
package buildgate

import (
	"context"
	"fmt"

	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/nzin/ai-software-factory/internal/agentkit"
	bg "github.com/nzin/ai-software-factory/internal/buildgate"
	"github.com/nzin/ai-software-factory/internal/factory"
	"github.com/nzin/ai-software-factory/internal/workspace"
)

// Executor builds the build-gate executor. It takes no llm.Client — the gate is
// entirely deterministic.
func Executor() a2asrv.AgentExecutor {
	return agentkit.DispatchExecutor(func(ctx context.Context, env factory.DispatchEnvelope) (factory.ResultEnvelope, error) {
		repo, err := workspace.Open(ctx, env.WorkspaceDir)
		if err != nil {
			return factory.ResultEnvelope{}, err
		}
		repo.BaseBranch = env.BaseBranch

		findings, summary := bg.Check(ctx, repo.Dir)
		for i := range findings {
			if findings[i].TargetRole == "" {
				findings[i].TargetRole = factory.RouteRole(findings, "")
			}
		}

		return factory.ResultEnvelope{
			Role:       env.Stage,
			Summary:    fmt.Sprintf("%s (%d findings)", summary, len(findings)),
			Findings:   findings,
			Verdict:    factory.VerdictFor(findings),
			TargetRole: factory.RouteRole(findings, ""),
		}, nil
	})
}
