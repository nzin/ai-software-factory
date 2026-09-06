package agentkit

import (
	"context"
	"iter"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/nzin/ai-software-factory/internal/llm"
)

// LLMExecutor returns an AgentExecutor that runs a single Claude turn: it
// concatenates the text parts of the incoming message, sends them to the model
// with systemPrompt, and yields the response as one terminal agent message.
//
// Every role agent in the factory (planner, developers, reviewers) has this
// shape; they differ only in the system prompt and the model configuration.
func LLMExecutor(client *llm.Client, systemPrompt string) a2asrv.AgentExecutor {
	return a2asrv.AgentExecutorFunc(func(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			prompt := messageText(ec.Message)
			if strings.TrimSpace(prompt) == "" {
				yield(nil, errEmptyInput)
				return
			}
			out, err := client.Complete(ctx, systemPrompt, prompt)
			if err != nil {
				yield(nil, err)
				return
			}
			yield(a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(out)), nil)
		}
	})
}

var errEmptyInput = errInput("agentkit: message contained no text")

type errInput string

func (e errInput) Error() string { return string(e) }

func messageText(msg *a2a.Message) string {
	if msg == nil {
		return ""
	}
	var b strings.Builder
	for _, p := range msg.Parts {
		if p == nil {
			continue
		}
		if t := p.Text(); t != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(t)
		}
	}
	return b.String()
}
