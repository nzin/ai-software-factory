package planner

import (
	"context"
	"iter"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/nzin/ai-software-factory/internal/agentkit"
	"github.com/nzin/ai-software-factory/internal/factory"
	"github.com/nzin/ai-software-factory/internal/llm"
)

// attachmentGlobs are the file extensions PRD evidence images may use,
// mirroring coordinator.AttachmentMediaTypes.
var attachmentGlobs = []string{"*.png", "*.jpg", "*.jpeg", "*.gif", "*.webp"}

// maxAttachments bounds how many evidence images go into one planning
// request, matching designreview.maxAssets.
const maxAttachments = 12

// Completer is the slice of *llm.Client the executor needs — a seam for tests.
type Completer interface {
	Complete(ctx context.Context, system, user string) (string, error)
	CompleteWithImages(ctx context.Context, system, user string, images []llm.ImageInput) (string, error)
}

// Executor builds the planner executor: a single Claude turn over the
// assembled prompt (see coordinator.pipelineEngine.plannerInput), plus vision
// on any evidence screenshots submitted with the PRD and committed under
// docs/prd/attachments/ in the run's workspace.
func Executor(client Completer, systemPrompt string) a2asrv.AgentExecutor {
	return a2asrv.AgentExecutorFunc(func(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			env, err := agentkit.DecodeDispatch(ec.Message)
			if err != nil {
				yield(nil, err)
				return
			}
			out, err := run(ctx, client, systemPrompt, env)
			if err != nil {
				yield(nil, err)
				return
			}
			yield(a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(out)), nil)
		}
	})
}

var errEmptyPrompt = planErr("agentkit: planner message carried no prompt text")

type planErr string

func (e planErr) Error() string { return string(e) }

// run does one Claude turn over env.PRDText, attaching vision on any evidence
// screenshots found under docs/prd/attachments/ in env.WorkspaceDir.
func run(ctx context.Context, client Completer, systemPrompt string, env factory.DispatchEnvelope) (string, error) {
	if strings.TrimSpace(env.PRDText) == "" {
		return "", errEmptyPrompt
	}
	images := loadAttachments(env.WorkspaceDir)
	if len(images) > 0 {
		return client.CompleteWithImages(ctx, systemPrompt, env.PRDText, images)
	}
	return client.Complete(ctx, systemPrompt, env.PRDText)
}

// loadAttachments reads any PRD evidence screenshots committed under
// docs/prd/attachments/ in workspaceDir, for Claude's vision input. It
// returns nil (not an error) when there is no workspace or no attachments —
// vision is a bonus, not a requirement, for planning.
func loadAttachments(workspaceDir string) []llm.ImageInput {
	if workspaceDir == "" {
		return nil
	}
	dir := filepath.Join(workspaceDir, "docs", "prd", "attachments")
	var paths []string
	for _, pattern := range attachmentGlobs {
		matches, _ := filepath.Glob(filepath.Join(dir, pattern))
		paths = append(paths, matches...)
	}
	sort.Strings(paths)
	if len(paths) > maxAttachments {
		paths = paths[:maxAttachments]
	}

	images := make([]llm.ImageInput, 0, len(paths))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		images = append(images, llm.ImageInput{MediaType: mediaTypeForExt(filepath.Ext(p)), Data: data})
	}
	return images
}

func mediaTypeForExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return "image/png"
	}
}
