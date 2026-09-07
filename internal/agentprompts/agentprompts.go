// Package agentprompts loads an agent's system prompt (and optional metadata)
// from a file: <dir>/<role>.md. The file may begin with a YAML front-matter
// block delimited by "---" lines; the rest is the system prompt.
package agentprompts

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	yaml "go.yaml.in/yaml/v3"

	"github.com/nzin/ai-software-factory/internal/modelext"
)

// Prompt is a loaded agent prompt file.
type Prompt struct {
	Role string

	// From front-matter (empty when not set).
	Name        string
	Description string
	Skills      []string

	// Model is the front-matter `model:` block, or nil when the file does not
	// declare one (the caller then falls back to modelext.Defaults).
	Model *modelext.Config

	// System is the prompt body (everything after the front-matter). Required.
	System string
}

type frontMatter struct {
	Name        string           `yaml:"name"`
	Description string           `yaml:"description"`
	Skills      []string         `yaml:"skills"`
	Model       *modelext.Config `yaml:"model"`
}

// Load reads dir/<role>.md and parses it.
func Load(dir, role string) (*Prompt, error) {
	if dir == "" {
		dir = "agent_prompts"
	}
	path := filepath.Join(dir, role+".md")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("agentprompts: read %s: %w", path, err)
	}
	p, err := parse(role, data)
	if err != nil {
		return nil, fmt.Errorf("agentprompts: %s: %w", path, err)
	}
	return p, nil
}

// parse splits optional front-matter from the body.
func parse(role string, data []byte) (*Prompt, error) {
	// Normalise CRLF so the delimiter checks are simple.
	norm := bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))

	p := &Prompt{Role: role}
	body := norm

	if bytes.HasPrefix(norm, []byte("---\n")) {
		rest := norm[len("---\n"):]
		end := bytes.Index(rest, []byte("\n---\n"))
		if end < 0 {
			// Also accept a trailing "---" with no newline after it.
			if bytes.HasSuffix(rest, []byte("\n---")) {
				end = len(rest) - len("\n---")
			}
		}
		if end < 0 {
			return nil, fmt.Errorf("front-matter opened with '---' but never closed")
		}
		var fm frontMatter
		if err := yaml.Unmarshal(rest[:end], &fm); err != nil {
			return nil, fmt.Errorf("front-matter: %w", err)
		}
		p.Name = strings.TrimSpace(fm.Name)
		p.Description = strings.TrimSpace(fm.Description)
		p.Skills = trimAll(fm.Skills)
		p.Model = fm.Model

		body = rest[end:]
		body = bytes.TrimPrefix(body, []byte("\n---\n"))
		body = bytes.TrimPrefix(body, []byte("\n---"))
	}

	p.System = strings.TrimSpace(string(body))
	if p.System == "" {
		return nil, fmt.Errorf("prompt body is empty")
	}
	return p, nil
}

func trimAll(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
