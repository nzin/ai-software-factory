package prd

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseMarkdown(t *testing.T) {
	p, err := ParseMarkdown("# Add dark mode\n\nUsers want a dark theme.\n\n- toggle in settings\n")
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "Add dark mode" {
		t.Fatalf("title = %q", p.Title)
	}
	if p.Description == "" {
		t.Fatal("description empty")
	}
}

func TestParseMarkdownNoH1(t *testing.T) {
	src := "## Background\n\nLet's build a game.\n\n## Requirements\n\n- one\n- two\n"
	p, err := ParseMarkdown(src)
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != untitled {
		t.Fatalf("title = %q, want %q", p.Title, untitled)
	}
	if !strings.Contains(p.Description, "## Background") || !strings.Contains(p.Description, "## Requirements") {
		t.Fatalf("description dropped headings: %q", p.Description)
	}
	if strings.Contains(p.Text(), "# ##") {
		t.Fatalf("Text() double-prefixed a heading:\n%s", p.Text())
	}
}

func TestTextDoesNotDoublePrefixHeadingTitle(t *testing.T) {
	// A run persisted before the ParseMarkdown fix still has a "## …" title.
	got := PRD{Title: "## Background", Description: "d"}.Text()
	if strings.HasPrefix(got, "# #") {
		t.Fatalf("Text() double-prefixed: %q", got)
	}
}

func TestParseJSON(t *testing.T) {
	p, err := ParseJSON([]byte(`{"title":"X","description":"d","acceptanceCriteria":["a","b"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "X" || len(p.AcceptanceCriteria) != 2 {
		t.Fatalf("bad prd: %+v", p)
	}
}

func TestAttachmentsRoundTripThroughJSON(t *testing.T) {
	p, err := ParseJSON([]byte(`{"title":"X","attachments":[
		{"path":"docs/prd/attachments/001-bug.png","filename":"bug.png","mediaType":"image/png"}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Attachments) != 1 {
		t.Fatalf("attachments = %+v", p.Attachments)
	}
	a := p.Attachments[0]
	if a.Path != "docs/prd/attachments/001-bug.png" || a.Filename != "bug.png" || a.MediaType != "image/png" {
		t.Fatalf("attachment = %+v", a)
	}

	out, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var got PRD
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Attachments) != 1 || got.Attachments[0] != a {
		t.Fatalf("round-tripped attachments = %+v", got.Attachments)
	}
}

func TestParseAutoDetect(t *testing.T) {
	if _, err := Parse(strings.NewReader(`{"title":"J"}`)); err != nil {
		t.Fatalf("json: %v", err)
	}
	if _, err := Parse(strings.NewReader("# M\n\nbody")); err != nil {
		t.Fatalf("markdown: %v", err)
	}
	if _, err := Parse(strings.NewReader("   ")); err == nil {
		t.Fatal("want error for empty PRD")
	}
}
