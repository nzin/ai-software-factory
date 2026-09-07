package agentkit

import (
	"reflect"
	"testing"

	"github.com/nzin/ai-software-factory/internal/agentprompts"
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
