package agentkit

import (
	"reflect"
	"testing"

	"github.com/nzin/ai-software-factory/internal/agentprompts"
	"github.com/nzin/ai-software-factory/internal/modelext"
)

func TestMergeMeta(t *testing.T) {
	opts := Options{
		Role:        "planner",
		Name:        "CodeName",
		Description: "code description",
		Skills:      []string{"a", "b"},
	}

	tests := []struct {
		name   string
		prompt *agentprompts.Prompt
		want   meta
	}{
		{
			name:   "no front-matter falls back to code defaults",
			prompt: &agentprompts.Prompt{Role: "planner"},
			want:   meta{Role: "planner", Name: "CodeName", Description: "code description", Skills: []string{"a", "b"}},
		},
		{
			name:   "front-matter wins field by field",
			prompt: &agentprompts.Prompt{Role: "planner", Name: "FileName", Skills: []string{"x"}},
			want:   meta{Role: "planner", Name: "FileName", Description: "code description", Skills: []string{"x"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeMeta(opts, tc.prompt)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("mergeMeta = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestMergeMetaNameFallsBackToRole(t *testing.T) {
	got := mergeMeta(Options{Role: "backend"}, &agentprompts.Prompt{Role: "backend"})
	if got.Name != "backend" {
		t.Fatalf("name = %q, want %q", got.Name, "backend")
	}
}

func TestResolveModel(t *testing.T) {
	fileModel := &modelext.Config{Model: "claude-sonnet-5", MaxTokens: 64000, Effort: "low"}

	// prompt file's model block wins over modelext.Defaults
	got := resolveModel(Options{Role: "backend-developer"}, &agentprompts.Prompt{Model: fileModel})
	if got.Model != "claude-sonnet-5" || got.MaxTokens != 64000 || got.Effort != "low" {
		t.Fatalf("file model not used: %+v", got)
	}
	if got.Thinking != "adaptive" { // WithDefaults fills the blank
		t.Fatalf("WithDefaults not applied: %+v", got)
	}

	// no model block -> modelext.Defaults
	got = resolveModel(Options{Role: "planner"}, &agentprompts.Prompt{})
	if got.Model != modelext.DefaultModel || got.MaxTokens != 16000 {
		t.Fatalf("fallback wrong: %+v", got)
	}

	// explicit Options.DefaultModel wins over the file
	got = resolveModel(Options{Role: "backend-developer", DefaultModel: modelext.Config{Model: "claude-opus-5"}},
		&agentprompts.Prompt{Model: fileModel})
	if got.Model != "claude-opus-5" {
		t.Fatalf("Options.DefaultModel should win: %+v", got)
	}
}
