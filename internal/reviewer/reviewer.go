package reviewer

import (
	"context"
	"fmt"

	"github.com/rokoucha/constulto/internal/config"
	"github.com/rokoucha/constulto/internal/runstore"
)

func ProbeAgent(ctx context.Context, agent config.Agent) Probe {
	switch agent.Adapter {
	case "claude":
		return ProbeClaude(ctx, agent.Command)
	case "muse":
		return probeMuse(ctx, agent.Command)
	case "opencode":
		return probeOpenCode(ctx, agent.Command)
	case "codex":
		return probeCodex(ctx, agent.Command)
	default:
		return Probe{Error: fmt.Sprintf("unsupported adapter %q", agent.Adapter)}
	}
}

func Run(ctx context.Context, agent config.Agent, workingDir string, prompt []byte) (*runstore.Review, Metadata, []byte, []byte, error) {
	switch agent.Adapter {
	case "claude":
		return runClaude(ctx, agent, workingDir, prompt)
	case "muse":
		return runMuse(ctx, agent, workingDir, prompt)
	case "opencode":
		return runOpenCode(ctx, agent, workingDir, prompt)
	case "codex":
		return runCodex(ctx, agent, workingDir, prompt)
	default:
		return nil, Metadata{}, nil, nil, fmt.Errorf("unsupported adapter %q", agent.Adapter)
	}
}
