package uiux

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/nzin/ai-software-factory/internal/factory"
	"github.com/nzin/ai-software-factory/internal/workspace"
)

// fakeCompleter returns a canned reply and records the prompt it saw.
type fakeCompleter struct {
	reply string
	calls []string
}

func (f *fakeCompleter) Complete(_ context.Context, _, user string) (string, error) {
	f.calls = append(f.calls, user)
	return f.reply, nil
}

func newRepo(t *testing.T) *workspace.Repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	m := workspace.NewManager(t.TempDir())
	repo, err := m.Prepare(context.Background(), "run-uiux1", workspace.ParseRepoURL("", "main"))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	return repo
}

func TestRunTextOnlyReplyUnchanged(t *testing.T) {
	fc := &fakeCompleter{reply: "## Screens\n\n- Home: lists items.\n"}
	env := factory.DispatchEnvelope{Stage: factory.RoleUIUXDesigner, PRDText: "build a todo app"}

	res, err := run(context.Background(), fc, "sys", env)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Summary != fc.reply {
		t.Fatalf("Summary = %q, want raw reply %q", res.Summary, fc.reply)
	}
	if res.CommitSHA != "" || len(res.FilesWritten) != 0 {
		t.Fatalf("expected no commit/files, got CommitSHA=%q FilesWritten=%v", res.CommitSHA, res.FilesWritten)
	}
}

func TestRunSpecAndMockupCommitted(t *testing.T) {
	repo := newRepo(t)
	fc := &fakeCompleter{reply: "" +
		"=== FILE: design/spec.md ===\n## Screens\n\n- Home: lists items.\n=== END FILE: design/spec.md ===\n" +
		"=== FILE: design/mockups/home.svg ===\n<svg></svg>\n=== END FILE: design/mockups/home.svg ===\n",
	}
	env := factory.DispatchEnvelope{
		Stage:        factory.RoleUIUXDesigner,
		WorkspaceDir: repo.Dir,
		PRDText:      "build a todo app",
	}

	res, err := run(context.Background(), fc, "sys", env)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.Contains(res.Summary, "=== FILE") {
		t.Fatalf("Summary leaked block markers: %q", res.Summary)
	}
	if res.Summary != "## Screens\n\n- Home: lists items." {
		t.Fatalf("Summary = %q", res.Summary)
	}
	if res.CommitSHA == "" {
		t.Fatal("CommitSHA empty")
	}
	if got := strings.Join(res.FilesWritten, ","); got != "design/mockups/home.svg,design/spec.md" {
		t.Fatalf("FilesWritten = %q", got)
	}
}

func TestRunTokensAndComponentsCommitted(t *testing.T) {
	repo := newRepo(t)
	fc := &fakeCompleter{reply: "" +
		"=== FILE: design/spec.md ===\n## Screens\n\n- Home: lists items.\n=== END FILE: design/spec.md ===\n" +
		"=== FILE: design/mockups/home.svg ===\n<svg></svg>\n=== END FILE: design/mockups/home.svg ===\n" +
		`=== FILE: design/tokens.json ===` + "\n" + `{"colors":{"primary":"#000"}}` + "\n" + `=== END FILE: design/tokens.json ===` + "\n" +
		`=== FILE: design/components.json ===` + "\n" + `[{"name":"Button"}]` + "\n" + `=== END FILE: design/components.json ===` + "\n",
	}
	env := factory.DispatchEnvelope{
		Stage:        factory.RoleUIUXDesigner,
		WorkspaceDir: repo.Dir,
		PRDText:      "build a todo app",
	}

	res, err := run(context.Background(), fc, "sys", env)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	want := "design/components.json,design/mockups/home.svg,design/spec.md,design/tokens.json"
	if got := strings.Join(res.FilesWritten, ","); got != want {
		t.Fatalf("FilesWritten = %q, want %q", got, want)
	}
	if res.CommitSHA == "" {
		t.Fatal("CommitSHA empty")
	}
}

func TestRunInvalidTokensJSONDropped(t *testing.T) {
	repo := newRepo(t)
	fc := &fakeCompleter{reply: "" +
		"=== FILE: design/spec.md ===\n## Screens\n\n- Home: lists items.\n=== END FILE: design/spec.md ===\n" +
		`=== FILE: design/tokens.json ===` + "\n" + `{"colors":{"primary":"#000",}}` + "\n" + `=== END FILE: design/tokens.json ===` + "\n",
	}
	env := factory.DispatchEnvelope{
		Stage:        factory.RoleUIUXDesigner,
		WorkspaceDir: repo.Dir,
		PRDText:      "build a todo app",
	}

	res, err := run(context.Background(), fc, "sys", env)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, p := range res.FilesWritten {
		if p == "design/tokens.json" {
			t.Fatalf("invalid design/tokens.json was committed: %v", res.FilesWritten)
		}
	}
	if res.CommitSHA == "" {
		t.Fatal("CommitSHA empty — spec.md alone should still commit")
	}
	if !strings.Contains(res.Summary, "design/tokens.json") {
		t.Fatalf("Summary does not mention the dropped file: %q", res.Summary)
	}
}

func TestDropInvalidJSON(t *testing.T) {
	cases := []struct {
		name    string
		files   map[string]string
		want    []string
		wantLen int
	}{
		{"empty map", map[string]string{}, nil, 0},
		{"all valid", map[string]string{
			"design/tokens.json":     `{"a":1}`,
			"design/components.json": `[]`,
		}, nil, 2},
		{"mixed", map[string]string{
			"design/tokens.json":     `{"a":1,}`,
			"design/components.json": `[]`,
		}, []string{"design/tokens.json"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dropped := dropInvalidJSON(tc.files)
			if got := strings.Join(dropped, ","); got != strings.Join(tc.want, ",") {
				t.Fatalf("dropped = %v, want %v", dropped, tc.want)
			}
			if len(tc.files) != tc.wantLen {
				t.Fatalf("files left = %d, want %d", len(tc.files), tc.wantLen)
			}
		})
	}
}

func TestRunBlocksWithoutSpecPathFallsBack(t *testing.T) {
	fc := &fakeCompleter{reply: "" +
		"=== FILE: design/mockups/home.svg ===\n<svg></svg>\n=== END FILE: design/mockups/home.svg ===\n",
	}
	env := factory.DispatchEnvelope{Stage: factory.RoleUIUXDesigner, PRDText: "build a todo app"}

	res, err := run(context.Background(), fc, "sys", env)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Summary != fc.reply {
		t.Fatalf("Summary = %q, want raw reply %q", res.Summary, fc.reply)
	}
	if res.CommitSHA != "" || len(res.FilesWritten) != 0 {
		t.Fatalf("expected no commit/files, got CommitSHA=%q FilesWritten=%v", res.CommitSHA, res.FilesWritten)
	}
}
