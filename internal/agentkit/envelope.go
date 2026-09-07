package agentkit

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/nzin/ai-software-factory/internal/factory"
)

// DispatchFunc is the role-specific body of a Phase-2 agent.
type DispatchFunc func(ctx context.Context, env factory.DispatchEnvelope) (factory.ResultEnvelope, error)

// DispatchExecutor adapts a DispatchFunc to an a2asrv.AgentExecutor: it decodes
// the incoming factory.DispatchEnvelope (from a DataPart, or JSON text), runs fn,
// and yields the factory.ResultEnvelope as a DataPart.
func DispatchExecutor(fn DispatchFunc) a2asrv.AgentExecutor {
	return a2asrv.AgentExecutorFunc(func(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			env, err := decodeDispatch(ec.Message)
			if err != nil {
				yield(nil, err)
				return
			}
			res, err := fn(ctx, env)
			if err != nil {
				yield(nil, err)
				return
			}
			yield(a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewDataPart(res)), nil)
		}
	})
}

func decodeDispatch(msg *a2a.Message) (factory.DispatchEnvelope, error) {
	if msg == nil {
		return factory.DispatchEnvelope{}, fmt.Errorf("agentkit: nil message")
	}
	for _, p := range msg.Parts {
		if p == nil {
			continue
		}
		if d := p.Data(); d != nil {
			return factory.Decode[factory.DispatchEnvelope](d)
		}
	}
	// Fall back to a JSON envelope in a text part.
	for _, p := range msg.Parts {
		if p == nil {
			continue
		}
		if t := p.Text(); t != "" {
			var env factory.DispatchEnvelope
			if json.Unmarshal([]byte(t), &env) == nil && env.Stage != "" {
				return env, nil
			}
		}
	}
	return factory.DispatchEnvelope{}, fmt.Errorf("agentkit: message carried no DispatchEnvelope")
}
