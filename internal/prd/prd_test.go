package prd

import (
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

func TestParseJSON(t *testing.T) {
	p, err := ParseJSON([]byte(`{"title":"X","description":"d","acceptanceCriteria":["a","b"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "X" || len(p.AcceptanceCriteria) != 2 {
		t.Fatalf("bad prd: %+v", p)
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
