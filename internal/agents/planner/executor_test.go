package planner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nzin/ai-software-factory/internal/factory"
	"github.com/nzin/ai-software-factory/internal/llm"
)

// fakeCompleter returns a canned reply and records which method was called.
type fakeCompleter struct {
	reply       string
	textCalls   int
	visionCalls int
	lastImgs    []llm.ImageInput
}

func (f *fakeCompleter) Complete(_ context.Context, _, _ string) (string, error) {
	f.textCalls++
	return f.reply, nil
}

func (f *fakeCompleter) CompleteWithImages(_ context.Context, _, _ string, images []llm.ImageInput) (string, error) {
	f.visionCalls++
	f.lastImgs = images
	return f.reply, nil
}

func TestRunWithoutAttachmentsUsesPlainComplete(t *testing.T) {
	fc := &fakeCompleter{reply: "# Plan"}
	env := factory.DispatchEnvelope{PRDText: "# Add dark mode"}

	out, err := run(context.Background(), fc, "sys", env)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out != "# Plan" {
		t.Fatalf("out = %q", out)
	}
	if fc.textCalls != 1 || fc.visionCalls != 0 {
		t.Fatalf("textCalls=%d visionCalls=%d, want 1/0", fc.textCalls, fc.visionCalls)
	}
}

func TestRunWithAttachmentsUsesVision(t *testing.T) {
	dir := t.TempDir()
	attDir := filepath.Join(dir, "docs", "prd", "attachments")
	if err := os.MkdirAll(attDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(attDir, "001-bug.png"), []byte("fakepng"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(attDir, "002-bug.jpg"), []byte("fakejpg"), 0o644); err != nil {
		t.Fatal(err)
	}

	fc := &fakeCompleter{reply: "# Plan grounded in screenshot"}
	env := factory.DispatchEnvelope{PRDText: "# Fix the broken button", WorkspaceDir: dir}

	out, err := run(context.Background(), fc, "sys", env)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out != "# Plan grounded in screenshot" {
		t.Fatalf("out = %q", out)
	}
	if fc.textCalls != 0 || fc.visionCalls != 1 {
		t.Fatalf("textCalls=%d visionCalls=%d, want 0/1", fc.textCalls, fc.visionCalls)
	}
	if len(fc.lastImgs) != 2 {
		t.Fatalf("images = %+v, want 2", fc.lastImgs)
	}
}

func TestRunRejectsEmptyPrompt(t *testing.T) {
	fc := &fakeCompleter{}
	if _, err := run(context.Background(), fc, "sys", factory.DispatchEnvelope{}); err == nil {
		t.Fatal("want error for empty prompt")
	}
}
