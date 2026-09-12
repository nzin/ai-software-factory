// Package designreview is the executor for the design-reviewer agent: it
// compares real e2e screenshots against the UI/UX designer's SVG mockups
// using Claude's native multimodal vision, and reports mismatches as
// findings a developer can act on. It only covers the web frontend — there
// is no mobile screenshot harness in this repo, so a mobile-only run always
// takes the no-op path below.
package designreview

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/nzin/ai-software-factory/internal/agentkit"
	"github.com/nzin/ai-software-factory/internal/factory"
	"github.com/nzin/ai-software-factory/internal/llm"
	"github.com/nzin/ai-software-factory/internal/workspace"
)

// maxAssets bounds how many mockups/screenshots go into one request, to keep
// request size and cost predictable on a run with many screens.
const maxAssets = 12

// Completer is the slice of *llm.Client the executor needs — a seam for tests.
type Completer interface {
	CompleteWithImages(ctx context.Context, system, user string, images []llm.ImageInput) (string, error)
}

const outputContract = `
After comparing the mockups against the screenshots, output ONLY a JSON array
of findings:

[
  { "severity": "high", "title": "short description", "suggestion": "how to fix",
    "targetRole": "frontend" }
]

severity is one of: critical, high, medium, low, info. Reserve critical/high
for a spec-required element that is entirely missing or contradicted in the
render (e.g. a required form field absent, wrong screen entirely).
Color/spacing/font/minor-copy differences are low or medium — this is a fuzzy
visual comparison, not a pixel diff.
targetRole is the developer who should fix it: frontend or mobile only, never
backend.
Empty array if the render matches the mockups.`

// Executor builds the design-reviewer executor.
func Executor(client Completer, systemPrompt string) a2asrv.AgentExecutor {
	return agentkit.DispatchExecutor(func(ctx context.Context, env factory.DispatchEnvelope) (factory.ResultEnvelope, error) {
		return run(ctx, client, systemPrompt, env)
	})
}

func run(ctx context.Context, client Completer, systemPrompt string, env factory.DispatchEnvelope) (factory.ResultEnvelope, error) {
	repo, err := workspace.Open(ctx, env.WorkspaceDir)
	if err != nil {
		return factory.ResultEnvelope{}, err
	}

	mockups := limit(glob(repo.Dir, "design/mockups/*.svg"), maxAssets)
	if len(mockups) == 0 {
		return factory.ResultEnvelope{
			Role:    env.Stage,
			Summary: "no design mockups to compare against — skipping design review",
			Verdict: factory.VerdictApprove,
		}, nil
	}
	shots := limit(glob(repo.Dir, "test/e2e/screenshots/*.png"), maxAssets)
	if len(shots) == 0 {
		return factory.ResultEnvelope{
			Role:    env.Stage,
			Summary: "no rendered e2e screenshots yet — skipping design review (mobile UI is not covered by this check)",
			Verdict: factory.VerdictApprove,
		}, nil
	}

	// Images are attached ahead of the text turn (see llm.CompleteWithImages),
	// so the text has to explain which image is which by position rather than
	// interleaving — the legend below is in the same order as images.
	images := make([]llm.ImageInput, 0, len(shots))
	var b strings.Builder
	b.WriteString("# Rendered screenshots (in the order attached below)\n\n")
	for i, rel := range shots {
		data, err := os.ReadFile(filepath.Join(repo.Dir, rel))
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "%d. %s\n", i+1, rel)
		images = append(images, llm.ImageInput{MediaType: "image/png", Data: data})
	}
	b.WriteString("\n# Design mockups to compare against\n\n")
	for _, rel := range mockups {
		body, err := os.ReadFile(filepath.Join(repo.Dir, rel))
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "### Mockup: %s\n\n%s\n\n", rel, body)
	}
	b.WriteString(outputContract)

	out, err := client.CompleteWithImages(ctx, systemPrompt, b.String(), images)
	if err != nil {
		return factory.ResultEnvelope{}, err
	}
	findings := factory.ParseFindings(out)
	for i := range findings {
		if findings[i].Source == "" {
			findings[i].Source = "design-reviewer"
		}
	}
	return factory.ResultEnvelope{
		Role:       env.Stage,
		Summary:    fmt.Sprintf("compared %d mockups against %d screenshots: %d findings", len(mockups), len(shots), len(findings)),
		Findings:   findings,
		Verdict:    factory.VerdictFor(findings),
		TargetRole: factory.RouteRole(findings, ""),
	}, nil
}

// glob returns pattern's matches under dir, relative to dir and slash-joined,
// sorted for deterministic ordering.
func glob(dir, pattern string) []string {
	matches, _ := filepath.Glob(filepath.Join(dir, pattern))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if rel, err := filepath.Rel(dir, m); err == nil {
			out = append(out, filepath.ToSlash(rel))
		}
	}
	sort.Strings(out)
	return out
}

// limit bounds xs to n elements.
func limit(xs []string, n int) []string {
	if len(xs) > n {
		return xs[:n]
	}
	return xs
}
