// Package prd defines the Product Requirements Definition and its parsers.
package prd

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// PRD is a feature request submitted to the factory.
type PRD struct {
	Title              string   `json:"title"`
	Description        string   `json:"description"`
	AcceptanceCriteria []string `json:"acceptanceCriteria,omitempty"`
}

// Validate checks the PRD is usable.
func (p PRD) Validate() error {
	if strings.TrimSpace(p.Title) == "" {
		return errors.New("prd: title is required")
	}
	return nil
}

// Text renders the PRD as a single prompt-friendly block.
func (p PRD) Text() string {
	var b strings.Builder
	if !strings.HasPrefix(strings.TrimSpace(p.Title), "#") {
		b.WriteString("# ")
	}
	b.WriteString(p.Title)
	b.WriteString("\n\n")
	if p.Description != "" {
		b.WriteString(p.Description)
		b.WriteString("\n")
	}
	if len(p.AcceptanceCriteria) > 0 {
		b.WriteString("\n## Acceptance criteria\n")
		for _, c := range p.AcceptanceCriteria {
			b.WriteString("- ")
			b.WriteString(c)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// ParseJSON decodes a PRD from JSON.
func ParseJSON(b []byte) (PRD, error) {
	var p PRD
	if err := json.Unmarshal(b, &p); err != nil {
		return PRD{}, err
	}
	return p, p.Validate()
}

// untitled is the placeholder title for a markdown PRD with no level-1 heading.
const untitled = "Untitled PRD"

// ParseMarkdown extracts a PRD from Markdown: the first level-1 heading is the
// title, everything after it is the description. A document whose first non-blank
// line is a deeper heading (## …) has no title of its own — it keeps the whole
// document as the description and takes the placeholder title.
func ParseMarkdown(md string) (PRD, error) {
	var p PRD
	sc := bufio.NewScanner(strings.NewReader(md))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var body []string
	titled := false
	for sc.Scan() {
		line := sc.Text()
		if !titled {
			trimmed := strings.TrimSpace(line)
			if h, ok := strings.CutPrefix(trimmed, "# "); ok {
				p.Title, titled = strings.TrimSpace(h), true
				continue
			}
			if trimmed == "" {
				continue
			}
			if strings.HasPrefix(trimmed, "#") {
				// A ## (or deeper) heading first: no title, keep the line.
				p.Title, titled = untitled, true
				body = append(body, line)
				continue
			}
			// No heading: treat the first non-blank line as the title.
			p.Title, titled = trimmed, true
			continue
		}
		body = append(body, line)
	}
	if err := sc.Err(); err != nil {
		return PRD{}, err
	}
	p.Description = strings.TrimSpace(strings.Join(body, "\n"))
	return p, p.Validate()
}

// Parse reads a PRD from r, auto-detecting JSON vs Markdown.
func Parse(r io.Reader) (PRD, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return PRD{}, err
	}
	trimmed := strings.TrimSpace(string(b))
	if strings.HasPrefix(trimmed, "{") {
		return ParseJSON(b)
	}
	return ParseMarkdown(string(b))
}
