package buildgate

import (
	"context"
	"errors"
	"testing"

	bg "github.com/nzin/ai-software-factory/internal/buildgate"
	"github.com/nzin/ai-software-factory/internal/factory"
)

type stubLLM struct {
	out string
	err error
}

func (s stubLLM) Complete(context.Context, string, string) (string, error) {
	return s.out, s.err
}

func cascade() bg.Result {
	// one root cause showing up as three downstream errors
	return bg.Result{
		Findings: []factory.Finding{
			{Source: "build-gate", Severity: "high", Category: "compile", File: "calc/calc.go", Line: 4, Title: "undefined: helper"},
			{Source: "build-gate", Severity: "high", Category: "compile", File: "api/handlers.go", Line: 9, Title: "undefined: helper"},
			{Source: "build-gate", Severity: "high", Category: "compile", File: "api/handlers.go", Line: 21, Title: "undefined: helper"},
		},
		Failures: []bg.Failure{{Kind: "go build", Output: "calc/calc.go:4: undefined: helper\napi/handlers.go:9: undefined: helper\napi/handlers.go:21: undefined: helper", DefaultRole: factory.RoleBackendDeveloper}},
	}
}

func TestTightenCollapsesToTheLLMFindings(t *testing.T) {
	llm := stubLLM{out: "```json\n[{\"severity\":\"high\",\"category\":\"compile\",\"file\":\"calc/calc.go\",\"line\":4,\"title\":\"helper is never defined\",\"suggestion\":\"add func helper(...)\",\"targetRole\":\"backend\"}]\n```"}

	got := tighten(context.Background(), llm, "sys", "diff", cascade())
	if len(got) != 1 {
		t.Fatalf("expected the cascade collapsed to 1 finding, got %d: %+v", len(got), got)
	}
	if got[0].File != "calc/calc.go" || got[0].Source != "build-gate" {
		t.Fatalf("finding lost detail: %+v", got[0])
	}
}

func TestTightenFallsBackOnLLMError(t *testing.T) {
	if got := tighten(context.Background(), stubLLM{err: errors.New("boom")}, "sys", "diff", cascade()); got != nil {
		t.Fatalf("an LLM error must return nil so the caller keeps the deterministic findings, got %+v", got)
	}
	if got := tighten(context.Background(), stubLLM{out: "no json here"}, "sys", "diff", cascade()); got != nil {
		t.Fatalf("unparseable output must return nil, got %+v", got)
	}
}

func TestRenderFindingsHandlesEmpty(t *testing.T) {
	if renderFindings(nil) == "" {
		t.Fatal("empty finding list should still produce guidance for the model")
	}
}
