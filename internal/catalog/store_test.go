package catalog

import (
	"context"
	"testing"

	"github.com/nzin/ai-software-factory/internal/modelext"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open("file:" + t.Name() + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestUpsertAndGet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a := Agent{Role: "planner", Name: "Planner", BaseURL: "http://x:1", Skills: []string{"planning"}, Concurrency: 3, Enabled: true}
	_, mc, err := s.Upsert(ctx, a, modelext.Config{Model: "claude-sonnet-5"}, false)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if mc.Model != "claude-sonnet-5" {
		t.Fatalf("model = %q", mc.Model)
	}

	got, gotMC, err := s.Get(ctx, "planner")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Concurrency != 3 || len(got.Skills) != 1 {
		t.Fatalf("bad agent: %+v", got)
	}
	if gotMC.MaxTokens != 16000 { // default filled in
		t.Fatalf("model defaults not applied: %+v", gotMC)
	}
}

func TestUpsertDoesNotOverwriteModelUnlessForced(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a := Agent{Role: "planner", Name: "P", BaseURL: "http://x:1", Enabled: true}

	if _, _, err := s.Upsert(ctx, a, modelext.Config{Model: "claude-sonnet-5"}, false); err != nil {
		t.Fatal(err)
	}
	// Operator override.
	if _, err := s.PatchModel(ctx, "planner", ModelPatch{Model: ptr("claude-opus-5")}); err != nil {
		t.Fatal(err)
	}
	// Re-register with a different default model, no force -> override survives.
	if _, _, err := s.Upsert(ctx, a, modelext.Config{Model: "claude-sonnet-5"}, false); err != nil {
		t.Fatal(err)
	}
	mc, _ := s.GetModel(ctx, "planner")
	if mc.Model != "claude-opus-5" {
		t.Fatalf("model overwritten: %q", mc.Model)
	}
	// With force -> replaced.
	if _, _, err := s.Upsert(ctx, a, modelext.Config{Model: "claude-sonnet-5"}, true); err != nil {
		t.Fatal(err)
	}
	mc, _ = s.GetModel(ctx, "planner")
	if mc.Model != "claude-sonnet-5" {
		t.Fatalf("force did not replace: %q", mc.Model)
	}
}

func TestListSkillFilter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, _, _ = s.Upsert(ctx, Agent{Role: "planner", Name: "P", BaseURL: "u", Skills: []string{"planning"}, Enabled: true}, modelext.Config{}, false)
	_, _, _ = s.Upsert(ctx, Agent{Role: "frontend", Name: "F", BaseURL: "u", Skills: []string{"vue", "css"}, Enabled: true}, modelext.Config{}, false)

	all, _ := s.List(ctx, "", nil)
	if len(all) != 2 {
		t.Fatalf("want 2 agents, got %d", len(all))
	}
	only, _ := s.List(ctx, "VUE", nil) // case-insensitive
	if len(only) != 1 || only[0].Role != "frontend" {
		t.Fatalf("skill filter: %+v", only)
	}
}

func TestGetNotFound(t *testing.T) {
	s := newTestStore(t)
	if _, _, err := s.Get(context.Background(), "nope"); err != ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func ptr[T any](v T) *T { return &v }
