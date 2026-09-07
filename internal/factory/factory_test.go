package factory

import "testing"

func TestParsePlanTasks(t *testing.T) {
	in := "# Plan\n\nProse here.\n\n```json\n{\"tasks\":[{\"id\":\"T1\",\"role\":\"backend\",\"title\":\"API\"},{\"id\":\"T2\",\"role\":\"Frontend Developer\",\"title\":\"SPA\"}]}\n```\n"
	tasks, prose := ParsePlanTasks(in)
	if len(tasks) != 2 {
		t.Fatalf("tasks = %d", len(tasks))
	}
	if prose != "# Plan\n\nProse here." {
		t.Fatalf("prose = %q", prose)
	}
	norm := NormalizeTasks(tasks)
	if norm[0].Role != RoleBackendDeveloper || norm[1].Role != RoleFrontendDev {
		t.Fatalf("roles = %q, %q", norm[0].Role, norm[1].Role)
	}
	if len(TasksForRole(norm, RoleBackendDeveloper)) != 1 {
		t.Fatal("TasksForRole backend")
	}
}

func TestParsePlanTasksNoBlock(t *testing.T) {
	tasks, prose := ParsePlanTasks("just prose, no json")
	if tasks != nil {
		t.Fatalf("tasks = %v", tasks)
	}
	if prose != "just prose, no json" {
		t.Fatalf("prose = %q", prose)
	}
}

func TestParsePlanTasksMalformed(t *testing.T) {
	in := "prose\n\n```json\n{not valid\n```"
	tasks, prose := ParsePlanTasks(in)
	if tasks != nil {
		t.Fatalf("tasks = %v", tasks)
	}
	if prose == "" {
		t.Fatal("prose should be preserved")
	}
}

func TestParseFileSpecs(t *testing.T) {
	out := "Here are the files:\n```json\n[{\"path\":\"main.go\",\"content\":\"package main\"},{\"path\":\"go.mod\",\"content\":\"module x\"}]\n```\ndone"
	files, err := ParseFileSpecs(out)
	if err != nil {
		t.Fatal(err)
	}
	if files["main.go"] != "package main" || files["go.mod"] != "module x" {
		t.Fatalf("files = %v", files)
	}
}

func TestExtractJSONNested(t *testing.T) {
	raw, ok := ExtractJSON(`prefix [{"a":{"b":[1,2]}}] suffix`)
	if !ok || string(raw) != `[{"a":{"b":[1,2]}}]` {
		t.Fatalf("got %q ok=%v", raw, ok)
	}
	// braces inside strings must not confuse the scanner
	raw, ok = ExtractJSON(`{"content":"func() { return }"}`)
	if !ok || string(raw) != `{"content":"func() { return }"}` {
		t.Fatalf("got %q ok=%v", raw, ok)
	}
}

func TestParsePlanApproval(t *testing.T) {
	in := "# Plan\n\n```json\n{\"tasks\":[{\"id\":\"T1\",\"role\":\"backend\",\"title\":\"API\"}]," +
		"\"approval\":{\"required\":true,\"reason\":\"touches auth code\"}}\n```\n"
	doc := ParsePlan(in)
	if !doc.Approval.Required || doc.Approval.Reason != "touches auth code" {
		t.Fatalf("approval = %+v", doc.Approval)
	}
	if len(doc.Tasks) != 1 || doc.Tasks[0].Role != RoleBackendDeveloper {
		t.Fatalf("tasks = %+v", doc.Tasks)
	}
}

func TestParsePlanApprovalAbsent(t *testing.T) {
	doc := ParsePlan("# Plan\n\n```json\n{\"tasks\":[]}\n```")
	if doc.Approval.Required {
		t.Fatalf("approval defaulted to required: %+v", doc.Approval)
	}
}

func TestRouteRole(t *testing.T) {
	findings := []Finding{
		{TargetRole: "backend", Title: "a"},
		{TargetRole: "frontend-developer", Title: "b"},
		{TargetRole: "frontend-developer", Title: "c"},
	}
	if got := RouteRole(findings, ""); got != RoleFrontendDev {
		t.Fatalf("most-referenced = %q, want frontend-developer", got)
	}
	if got := RouteRole(nil, RoleBackendDeveloper); got != RoleBackendDeveloper {
		t.Fatalf("fallback to lastDev = %q", got)
	}
	if got := RouteRole(nil, "planner"); got != RoleBackendDeveloper {
		t.Fatalf("final fallback = %q, want backend-developer", got)
	}
}

func TestVerdictFor(t *testing.T) {
	if VerdictFor(nil) != VerdictApprove {
		t.Fatal("empty -> approve")
	}
	if VerdictFor([]Finding{{Severity: "low"}, {Severity: "info"}}) != VerdictApprove {
		t.Fatal("low/info -> approve")
	}
	if VerdictFor([]Finding{{Severity: "low"}, {Severity: "high"}}) != VerdictRequestChanges {
		t.Fatal("any high -> request_changes")
	}
	if VerdictFor([]Finding{{Severity: "CRITICAL"}}) != VerdictRequestChanges {
		t.Fatal("case-insensitive critical -> request_changes")
	}
}

func TestParseFindings(t *testing.T) {
	fs := ParseFindings("analysis...\n[{\"severity\":\"high\",\"file\":\"a.go\",\"line\":3,\"title\":\"bad\"}]")
	if len(fs) != 1 || fs[0].Severity != "high" || fs[0].Line != 3 {
		t.Fatalf("findings = %+v", fs)
	}
}
