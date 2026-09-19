package reviewer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/rokoucha/constulto/assets"
	"github.com/rokoucha/constulto/internal/config"
	"github.com/rokoucha/constulto/internal/runstore"
)

const outputLimit = 16 << 20

var (
	ErrAgentFailed    = errors.New("agent failed")
	ErrAuthentication = errors.New("authentication failed")
	ErrPermission     = errors.New("permission denied")
	ErrRateLimit      = errors.New("rate limit exceeded")
	ErrInvalidOutput  = errors.New("invalid output")
	ErrOutputLimit    = errors.New("output limit exceeded")
)

type Probe struct {
	Version   string `json:"version"`
	Supported bool   `json:"supported"`
	Error     string `json:"error,omitempty"`
}

type Metadata struct {
	Model           string
	ObservedModels  []string
	ProviderCostUSD *float64
}

func ProbeClaude(ctx context.Context, command string) Probe {
	version, err := exec.CommandContext(ctx, command, "--version").CombinedOutput()
	if err != nil {
		return Probe{Error: strings.TrimSpace(string(version)) + ": " + err.Error()}
	}
	help, err := exec.CommandContext(ctx, command, "--help").CombinedOutput()
	if err != nil {
		return Probe{Version: strings.TrimSpace(string(version)), Error: err.Error()}
	}
	required := []string{
		"--print", "--safe-mode", "--restricted", "--strict-mcp-config", "--mcp-config",
		"--tools", "--permission-mode", "--permission-prompts", "--disable-slash-commands",
		"--no-session-persistence", "--output-format", "--json-schema", "--model",
	}
	for _, flag := range required {
		if !bytes.Contains(help, []byte(flag)) {
			return Probe{Version: strings.TrimSpace(string(version)), Error: "required capability missing: " + flag}
		}
	}
	return Probe{Version: strings.TrimSpace(string(version)), Supported: true}
}

func runClaude(ctx context.Context, agent config.Agent, workingDir string, prompt []byte) (*runstore.Review, Metadata, []byte, []byte, error) {
	schema, err := assets.FS.ReadFile("schema/review-result.json")
	if err != nil {
		return nil, Metadata{}, nil, nil, err
	}
	schema, err = claudeSchema(schema)
	if err != nil {
		return nil, Metadata{}, nil, nil, err
	}
	args := []string{"--print", "--safe-mode", "--restricted", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`,
		"--tools", "Read,Glob,Grep", "--permission-mode", "dontAsk", "--permission-prompts", "none",
		"--disable-slash-commands", "--no-session-persistence", "--output-format", "json", "--json-schema", string(schema)}
	if agent.Model != "" {
		args = append(args, "--model", agent.Model)
	}
	cmd := exec.Command(agent.Command, args...)
	cmd.Dir = workingDir
	cmd.Stdin = bytes.NewReader(prompt)
	cmd.Env = childEnv(os.Environ())
	stdout, stderr, runErr := runCommand(ctx, cmd)
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			runErr = nativeFailure(stdout, runErr)
		}
		return nil, Metadata{}, stdout, stderr, runErr
	}
	review, metadata, err := parseClaude(stdout)
	if err != nil {
		return nil, Metadata{}, stdout, stderr, err
	}
	return review, metadata, stdout, stderr, nil
}

func nativeFailure(raw []byte, exitErr error) error {
	var envelope struct {
		Result string `json:"result"`
	}
	if json.Unmarshal(raw, &envelope) == nil && envelope.Result != "" {
		return classifyMessage(envelope.Result, exitErr)
	}
	return fmt.Errorf("%w: Claude exited unsuccessfully: %v", ErrAgentFailed, exitErr)
}

// claudeSchema removes document metadata that Claude Code 2.1.x attempts to
// resolve as a remote metaschema. The full schema remains the local contract.
func claudeSchema(schema []byte) ([]byte, error) {
	var document map[string]any
	if err := json.Unmarshal(schema, &document); err != nil {
		return nil, fmt.Errorf("decode review schema: %w", err)
	}
	delete(document, "$schema")
	delete(document, "$id")
	converted, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode Claude schema: %w", err)
	}
	return converted, nil
}

func parseClaude(raw []byte) (*runstore.Review, Metadata, error) {
	var envelope struct {
		Structured   json.RawMessage `json:"structured_output"`
		Result       json.RawMessage `json:"result"`
		IsError      bool            `json:"is_error"`
		TotalCostUSD *float64        `json:"total_cost_usd"`
		ModelUsage   map[string]struct {
			OutputTokens int `json:"outputTokens"`
		} `json:"modelUsage"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, Metadata{}, fmt.Errorf("%w: invalid Claude JSON envelope: %v", ErrInvalidOutput, err)
	}
	if envelope.IsError {
		var message string
		if json.Unmarshal(envelope.Result, &message) != nil {
			message = string(envelope.Result)
		}
		return nil, Metadata{}, classifyMessage(message, ErrAgentFailed)
	}
	payload := envelope.Structured
	if len(payload) == 0 || bytes.Equal(payload, []byte("null")) {
		if len(envelope.Result) == 0 {
			return nil, Metadata{}, fmt.Errorf("%w: Claude returned no structured result", ErrInvalidOutput)
		}
		var text string
		if json.Unmarshal(envelope.Result, &text) == nil {
			payload = []byte(text)
		} else {
			payload = envelope.Result
		}
	}
	if err := config.Validate("schema/review-result.json", payload); err != nil {
		return nil, Metadata{}, fmt.Errorf("%w: invalid structured review: %v", ErrInvalidOutput, err)
	}
	var review runstore.Review
	if err := json.Unmarshal(payload, &review); err != nil {
		return nil, Metadata{}, err
	}
	metadata := Metadata{}
	if envelope.TotalCostUSD != nil && *envelope.TotalCostUSD >= 0 && !math.IsNaN(*envelope.TotalCostUSD) && !math.IsInf(*envelope.TotalCostUSD, 0) {
		metadata.ProviderCostUSD = envelope.TotalCostUSD
	}
	bestTokens := -1
	for model, usage := range envelope.ModelUsage {
		metadata.ObservedModels = append(metadata.ObservedModels, model)
		if usage.OutputTokens > bestTokens {
			metadata.Model, bestTokens = model, usage.OutputTokens
		}
	}
	sort.Strings(metadata.ObservedModels)
	return &review, metadata, nil
}

func IsTimeout(err error) bool { return errors.Is(err, context.DeadlineExceeded) }

func ErrorKind(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, ErrInvalidOutput):
		return "invalid-output"
	case errors.Is(err, ErrOutputLimit):
		return "output-limit"
	case errors.Is(err, ErrAuthentication):
		return "authentication"
	case errors.Is(err, ErrPermission):
		return "permission"
	case errors.Is(err, ErrRateLimit):
		return "rate-limit"
	case errors.Is(err, ErrAgentFailed):
		return "agent-failed"
	default:
		return "internal"
	}
}
