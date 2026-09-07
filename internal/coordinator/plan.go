package coordinator

import "github.com/nzin/ai-software-factory/internal/factory"

// devOrder is the fixed order developer stages run in.
var devOrder = []string{factory.RoleBackendDeveloper, factory.RoleFrontendDev, factory.RoleMobileDeveloper}

func isReviewer(stage string) bool {
	return stage == factory.RoleSecurityReviewer || stage == factory.RoleCodeReviewer
}

// plannedStages is the ordered post-planner pipeline for a run, derived from the
// planner's task list: an optional ui-ux-designer, the developer roles that have
// tasks, then the two reviewers.
func plannedStages(tasks []factory.PlanTask) []string {
	devs := developerStages(tasks)
	var stages []string
	if needsDesigner(devs) {
		stages = append(stages, factory.RoleUIUXDesigner)
	}
	stages = append(stages, devs...)
	stages = append(stages, factory.RoleSecurityReviewer, factory.RoleCodeReviewer)
	return stages
}

// developerStages returns the developer roles that have tasks, in devOrder. With
// no tasks at all it falls back to backend + frontend.
func developerStages(tasks []factory.PlanTask) []string {
	var out []string
	for _, role := range devOrder {
		if len(factory.TasksForRole(tasks, role)) > 0 {
			out = append(out, role)
		}
	}
	if len(out) == 0 {
		return []string{factory.RoleBackendDeveloper, factory.RoleFrontendDev}
	}
	return out
}

func needsDesigner(devs []string) bool {
	for _, d := range devs {
		if d == factory.RoleFrontendDev || d == factory.RoleMobileDeveloper {
			return true
		}
	}
	return false
}

// firstDevelopmentStage is where a run resumes after the approval gate (or falls
// through to when no approval is needed).
func firstDevelopmentStage(run *Run) string {
	if s := plannedStages(run.PlanTasks); len(s) > 0 {
		return s[0]
	}
	return ""
}

// nextStage returns the stage after run.Stage in the pipeline, or "" when the
// pipeline is complete. A developer fix pass (Attempts > 0) jumps straight to the
// security reviewer rather than re-running later developers.
func nextStage(run *Run) string {
	stages := plannedStages(run.PlanTasks)
	i := indexOf(stages, run.Stage)
	if i < 0 {
		return ""
	}
	if factory.IsDeveloperRole(run.Stage) && run.Attempts[run.Stage] > 0 {
		return factory.RoleSecurityReviewer
	}
	if i+1 < len(stages) {
		return stages[i+1]
	}
	return ""
}

func indexOf(ss []string, s string) int {
	for i, x := range ss {
		if x == s {
			return i
		}
	}
	return -1
}
