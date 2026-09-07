package agentprompts

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, role, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, role+".md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFrontMatter(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "planner", "---\nname: Planner\ndescription: plans stuff\nskills: [planning, architecture]\n---\nYou are a planner.\nSecond line.\n")

	p, err := Load(dir, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Planner" || p.Description != "plans stuff" {
		t.Fatalf("meta: %+v", p)
	}
	if len(p.Skills) != 2 || p.Skills[0] != "planning" {
		t.Fatalf("skills: %v", p.Skills)
	}
	if p.System != "You are a planner.\nSecond line." {
		t.Fatalf("system = %q", p.System)
	}
}

func TestNoFrontMatter(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "backend", "You are a backend developer.\n")

	p, err := Load(dir, "backend")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "" || p.Description != "" || len(p.Skills) != 0 || p.Model != nil {
		t.Fatalf("expected no meta: %+v", p)
	}
	if p.System != "You are a backend developer." {
		t.Fatalf("system = %q", p.System)
	}
}

func TestModelBlock(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "backend-developer",
		"---\nname: Backend\nmodel:\n  provider: anthropic\n  model: claude-sonnet-5\n  maxTokens: 64000\n  effort: low\n  thinking: adaptive\n---\nbody\n")

	p, err := Load(dir, "backend-developer")
	if err != nil {
		t.Fatal(err)
	}
	if p.Model == nil {
		t.Fatal("model block not parsed")
	}
	if p.Model.Model != "claude-sonnet-5" || p.Model.MaxTokens != 64000 || p.Model.Effort != "low" {
		t.Fatalf("model = %+v", *p.Model)
	}

	// front-matter without a model block -> nil
	write(t, dir, "planner", "---\nname: Planner\n---\nbody\n")
	p2, _ := Load(dir, "planner")
	if p2.Model != nil {
		t.Fatalf("expected nil model, got %+v", *p2.Model)
	}
}

func TestCRLF(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "x", "---\r\nname: X\r\n---\r\nprompt body\r\n")
	p, err := Load(dir, "x")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "X" || p.System != "prompt body" {
		t.Fatalf("got %+v", p)
	}
}

func TestErrors(t *testing.T) {
	dir := t.TempDir()

	if _, err := Load(dir, "missing"); err == nil {
		t.Fatal("want error for missing file")
	}

	write(t, dir, "empty", "---\nname: E\n---\n   \n")
	if _, err := Load(dir, "empty"); err == nil {
		t.Fatal("want error for empty body")
	}

	write(t, dir, "unterminated", "---\nname: U\nno closing delimiter\n")
	if _, err := Load(dir, "unterminated"); err == nil {
		t.Fatal("want error for unterminated front-matter")
	}
}
