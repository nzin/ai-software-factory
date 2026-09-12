// Package factory holds the message contracts exchanged between the coordinator
// and the specialized agents. It depends on neither, so both can import it.
package factory

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
)

// Roles used across the factory (stable catalog keys).
const (
	RolePlanner          = "planner"
	RoleUIUXDesigner     = "ui-ux-designer"
	RoleBackendDeveloper = "backend-developer"
	RoleFrontendDev      = "frontend-developer"
	RoleMobileDeveloper  = "mobile-developer"
	RoleTestEngineer     = "test-engineer"
	RoleBuildGate        = "build-gate"
	RoleSecurityReviewer = "security-reviewer"
	RoleCodeReviewer     = "code-reviewer"
)

// DeveloperRoles are the roles that write code into the worktree.
var DeveloperRoles = []string{RoleBackendDeveloper, RoleFrontendDev, RoleMobileDeveloper}

// RawFailuresDir is the workspace-relative directory build-gate writes raw
// (unsummarized) command failure output to — see internal/agents/buildgate.
// Named here, in the shared contracts package, so devagent can exclude it from
// the current-repository content it blindly inlines into every prompt without
// creating a dependency between the two agent packages.
const RawFailuresDir = "build-gate-raw"

// PlanTask is one unit of work the planner emits for a developer role.
type PlanTask struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Role    string `json:"role"`
	Details string `json:"details,omitempty"`
}

// Finding is an issue raised by a reviewer or tool.
type Finding struct {
	Source     string `json:"source"`             // "kodus", "gosec", "govulncheck", "npm-audit", "security-reviewer", "human"
	Severity   string `json:"severity,omitempty"` // "critical" | "high" | "medium" | "low" | "info"
	Category   string `json:"category,omitempty"`
	File       string `json:"file,omitempty"`
	Line       int    `json:"line,omitempty"`
	Title      string `json:"title"`
	Suggestion string `json:"suggestion,omitempty"`
	// Evidence is a verbatim excerpt of the actual failure — the real error
	// message / call log / assertion output the title/suggestion are derived
	// from, not a paraphrase. It exists so a title can be checked against the
	// text it's supposedly summarizing, instead of being trusted at face value.
	Evidence   string `json:"evidence,omitempty"`
	TargetRole string `json:"targetRole,omitempty"` // Phase 3: which role should fix it
}

// DispatchEnvelope is the coordinator -> agent payload (an a2a DataPart).
type DispatchEnvelope struct {
	RunID        string     `json:"runId"`
	Stage        string     `json:"stage"`
	WorkspaceDir string     `json:"workspaceDir"`
	BaseBranch   string     `json:"baseBranch"`
	WorkBranch   string     `json:"workBranch,omitempty"`
	RepoURL      string     `json:"repoUrl,omitempty"`
	RepoKind     string     `json:"repoKind,omitempty"` // new | local | remote
	PRDText      string     `json:"prdText,omitempty"`
	Plan         string     `json:"plan,omitempty"`
	UISpec       string     `json:"uiSpec,omitempty"`
	Tasks        []PlanTask `json:"tasks,omitempty"`
	// Findings and Attempt are set when a reviewer bounced the work back for a
	// fix pass.
	Findings []Finding `json:"findings,omitempty"`
	Attempt  int       `json:"attempt,omitempty"`
}

// Verdict values a reviewer returns.
const (
	VerdictApprove        = "approve"
	VerdictRequestChanges = "request_changes"
)

// ResultEnvelope is the agent -> coordinator payload (an a2a DataPart).
type ResultEnvelope struct {
	Role         string    `json:"role"`
	Summary      string    `json:"summary"`
	CommitSHA    string    `json:"commitSha,omitempty"`
	FilesWritten []string  `json:"filesWritten,omitempty"`
	Findings     []Finding `json:"findings,omitempty"`
	// Reviewers only:
	Verdict    string `json:"verdict,omitempty"`
	TargetRole string `json:"targetRole,omitempty"`
}

// ApprovalDecision is the planner's call on whether a human must sign off before
// development starts.
type ApprovalDecision struct {
	Required bool   `json:"required"`
	Reason   string `json:"reason,omitempty"`
}

// PlanDoc is the planner's structured output.
type PlanDoc struct {
	Prose    string           `json:"-"`
	Tasks    []PlanTask       `json:"tasks"`
	Approval ApprovalDecision `json:"approval"`
}

// ParsePlan pulls the planner's trailing ```json block ({tasks, approval}) and
// returns the tasks (role-normalised), the approval decision, and the prose plan
// with the JSON removed. Tolerant: a missing/broken block => no tasks, approval
// not required, prose unchanged.
func ParsePlan(plannerOutput string) PlanDoc {
	matches := fencedJSON.FindAllStringSubmatchIndex(plannerOutput, -1)
	if len(matches) == 0 {
		return PlanDoc{Prose: strings.TrimSpace(plannerOutput)}
	}
	m := matches[len(matches)-1]
	raw := plannerOutput[m[2]:m[3]]

	var parsed struct {
		Tasks    []PlanTask       `json:"tasks"`
		Approval ApprovalDecision `json:"approval"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return PlanDoc{Prose: strings.TrimSpace(plannerOutput)}
	}
	prose := strings.TrimSpace(plannerOutput[:m[0]] + plannerOutput[m[1]:])
	return PlanDoc{
		Prose:    prose,
		Tasks:    NormalizeTasks(parsed.Tasks),
		Approval: parsed.Approval,
	}
}

// RouteRole picks the developer to bounce a fix pass back to: the role most
// findings point at, else lastDev, else backend-developer.
func RouteRole(findings []Finding, lastDev string) string {
	count := map[string]int{}
	for _, f := range findings {
		if r := NormalizeRole(f.TargetRole); IsDeveloperRole(r) {
			count[r]++
		}
	}
	best, bestN := "", 0
	for r, n := range count {
		if n > bestN {
			best, bestN = r, n
		}
	}
	if best != "" {
		return best
	}
	if IsDeveloperRole(lastDev) {
		return lastDev
	}
	return RoleBackendDeveloper
}

// RoleForPath maps a repo-relative file path to the developer role that owns it,
// or "" when it is not a code file any single developer owns. Used to give an
// owner to findings that a tool (gosec, govulncheck, npm audit, the build gate)
// reports without one.
func RoleForPath(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return RoleBackendDeveloper
	case ".vue", ".ts", ".tsx", ".jsx", ".css", ".scss":
		return RoleFrontendDev
	}
	if strings.HasSuffix(path, "go.mod") || strings.HasSuffix(path, "go.sum") {
		return RoleBackendDeveloper
	}
	if strings.HasSuffix(path, "package.json") {
		return RoleFrontendDev
	}
	return ""
}

// VerdictFor derives a reviewer verdict from finding severities: request_changes
// if any finding is critical or high, else approve.
func VerdictFor(findings []Finding) string {
	for _, f := range findings {
		switch strings.ToLower(f.Severity) {
		case "critical", "high":
			return VerdictRequestChanges
		}
	}
	return VerdictApprove
}

// IsDeveloperRole reports whether r is one of the code-writing roles.
func IsDeveloperRole(r string) bool {
	for _, d := range DeveloperRoles {
		if d == r {
			return true
		}
	}
	return false
}

// Decode re-hydrates an envelope from the `any` returned by a2a's part.Data()
// (which is a map[string]any after JSON round-trip).
func Decode[T any](v any) (T, error) {
	var out T
	b, err := json.Marshal(v)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(b, &out)
	return out, err
}

var fencedJSON = regexp.MustCompile("(?s)```json\\s*(.*?)```")

// ParsePlanTasks is a thin wrapper over ParsePlan kept for callers that only
// need the tasks and prose.
func ParsePlanTasks(plannerOutput string) ([]PlanTask, string) {
	d := ParsePlan(plannerOutput)
	return d.Tasks, d.Prose
}

// TasksForRole filters a task list to one role.
func TasksForRole(tasks []PlanTask, role string) []PlanTask {
	var out []PlanTask
	for _, t := range tasks {
		if NormalizeRole(t.Role) == role {
			out = append(out, t)
		}
	}
	return out
}

// NormalizeRole maps loose planner output ("backend", "Backend Developer") to a
// canonical role key.
func NormalizeRole(r string) string {
	switch strings.ToLower(strings.TrimSpace(r)) {
	case "backend", "backend-developer", "backend developer", "api":
		return RoleBackendDeveloper
	case "frontend", "frontend-developer", "frontend developer", "web", "ui":
		return RoleFrontendDev
	case "mobile", "mobile-developer", "mobile developer", "ios", "android":
		return RoleMobileDeveloper
	case "test", "tests", "qa", "test-engineer", "test engineer", "component-test", "integration":
		return RoleTestEngineer
	default:
		return strings.ToLower(strings.TrimSpace(r))
	}
}

// NormalizeTasks canonicalizes the Role field of every task.
func NormalizeTasks(tasks []PlanTask) []PlanTask {
	out := make([]PlanTask, len(tasks))
	for i, t := range tasks {
		t.Role = NormalizeRole(t.Role)
		out[i] = t
	}
	return out
}

// FileSpec is one file an agent wants written into the worktree.
type FileSpec struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// ExtractJSON returns the first top-level JSON array or object embedded in s
// (handling ```json fences and surrounding prose).
func ExtractJSON(s string) ([]byte, bool) {
	if m := fencedJSON.FindStringSubmatch(s); len(m) == 2 {
		s = m[1]
	}
	openIdx := -1
	var closeCh byte
	for i := 0; i < len(s); i++ {
		if s[i] == '[' || s[i] == '{' {
			openIdx = i
			if s[i] == '[' {
				closeCh = ']'
			} else {
				closeCh = '}'
			}
			break
		}
	}
	if openIdx < 0 {
		return nil, false
	}
	depth := 0
	inStr, esc := false, false
	for i := openIdx; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case s[openIdx]:
			depth++
		case closeCh:
			depth--
			if depth == 0 {
				return []byte(s[openIdx : i+1]), true
			}
		}
	}
	return nil, false
}

// ParseFileSpecs pulls a JSON array of {path, content} out of an LLM response.
func ParseFileSpecs(llmOutput string) (map[string]string, error) {
	raw, ok := ExtractJSON(llmOutput)
	if !ok {
		return nil, errNoJSON
	}
	var specs []FileSpec
	if err := json.Unmarshal(raw, &specs); err != nil {
		// maybe {files:[...]}
		var wrap struct {
			Files []FileSpec `json:"files"`
		}
		if json.Unmarshal(raw, &wrap) != nil || len(wrap.Files) == 0 {
			return nil, err
		}
		specs = wrap.Files
	}
	files := make(map[string]string, len(specs))
	for _, s := range specs {
		if s.Path != "" {
			files[s.Path] = s.Content
		}
	}
	return files, nil
}

// BlockResult reports what ParseFileBlocks saw beyond the files themselves.
type BlockResult struct {
	Count     int    // complete "=== FILE: … ===" blocks parsed
	Truncated bool   // a block was opened but EOF arrived before its close
	LastPath  string // path of the unterminated block, when Truncated
}

const (
	fileBlockOpenPrefix  = "=== FILE: "
	fileBlockClosePrefix = "=== END FILE: "
	fileBlockSuffix      = " ==="
)

// ParseFileBlocks reads an escaping-free file list:
//
//	=== FILE: relative/path ===
//	<verbatim content>
//	=== END FILE: relative/path ===
//
// Content between the markers is taken literally — no JSON string escaping, so a
// model can never corrupt it with an unescaped newline or quote. Lines outside
// any block are ignored. When a block is opened but EOF arrives before its
// matching close, BlockResult.Truncated is set and the files that did complete
// are still returned. With no "=== FILE: " marker at all it returns
// (nil, &BlockResult{}) so the caller can fall back to ParseFileSpecs.
func ParseFileBlocks(s string) (map[string]string, *BlockResult) {
	res := &BlockResult{}
	files := map[string]string{}
	var (
		inBlock bool
		curPath string
		curBody []string
	)
	for _, line := range strings.Split(s, "\n") {
		marker := strings.TrimRight(line, " \t\r")
		switch {
		case !inBlock && strings.HasPrefix(marker, fileBlockOpenPrefix) && strings.HasSuffix(marker, fileBlockSuffix):
			curPath = strings.TrimSpace(marker[len(fileBlockOpenPrefix) : len(marker)-len(fileBlockSuffix)])
			curBody = curBody[:0]
			inBlock = curPath != ""
		case inBlock && marker == fileBlockClosePrefix+curPath+fileBlockSuffix:
			body := strings.Join(curBody, "\n")
			if body != "" {
				body += "\n"
			}
			files[curPath] = body
			res.Count++
			inBlock, curPath = false, ""
		case inBlock:
			curBody = append(curBody, line)
		}
	}
	if inBlock {
		res.Truncated, res.LastPath = true, curPath
	}
	if res.Count == 0 && !res.Truncated {
		return nil, res
	}
	return files, res
}

// ParseFindings pulls a JSON array of Finding out of an LLM response.
func ParseFindings(llmOutput string) []Finding {
	raw, ok := ExtractJSON(llmOutput)
	if !ok {
		return nil
	}
	var fs []Finding
	if json.Unmarshal(raw, &fs) != nil {
		var wrap struct {
			Findings []Finding `json:"findings"`
		}
		if json.Unmarshal(raw, &wrap) != nil {
			return nil
		}
		fs = wrap.Findings
	}
	return fs
}

var errNoJSON = jsonErr("no JSON array/object found in model output")

type jsonErr string

func (e jsonErr) Error() string { return string(e) }
