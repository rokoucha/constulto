package reviewer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/rokoucha/constulto/assets"
	"github.com/rokoucha/constulto/internal/config"
	"github.com/rokoucha/constulto/internal/runstore"
)

func probeAntigravity(ctx context.Context, command string) Probe {
	version, err := exec.CommandContext(ctx, command, "--version").CombinedOutput()
	if err != nil {
		return Probe{Error: strings.TrimSpace(string(version)) + ": " + err.Error()}
	}
	versionText := strings.TrimSpace(string(version))
	help, err := exec.CommandContext(ctx, command, "--help").CombinedOutput()
	if err != nil {
		return Probe{Version: versionText, Error: err.Error()}
	}
	for _, flag := range []string{"--input-format", "--output-format", "--json-schema", "--model", "--mode", "--sandbox", "--disable-slash-commands"} {
		if !bytes.Contains(help, []byte(flag)) {
			return Probe{Version: versionText, Error: "required capability missing: " + flag}
		}
	}
	return Probe{Version: versionText, Supported: true}
}

func runAntigravity(ctx context.Context, agent config.Agent, workingDir string, prompt []byte) (*runstore.Review, Metadata, []byte, []byte, error) {
	schema, err := assets.FS.ReadFile("schema/review-result.json")
	if err != nil {
		return nil, Metadata{}, nil, nil, err
	}
	schema, err = claudeSchema(schema)
	if err != nil {
		return nil, Metadata{}, nil, nil, err
	}
	args := []string{"--mode=plan", "--sandbox", "--disable-slash-commands", "--input-format", "stream-json", "--output-format", "stream-json", "--json-schema", string(schema)}
	if agent.Model != "" {
		args = append(args, "--model", agent.Model)
	}
	cmd := exec.Command(agent.Command, args...)
	cmd.Dir = workingDir
	input, err := json.Marshal(map[string]any{"event": "user", "message": map[string]string{"content": string(prompt)}})
	if err != nil {
		return nil, Metadata{}, nil, nil, err
	}
	cmd.Stdin = bytes.NewReader(append(input, '\n'))
	cmd.Env = childEnv(os.Environ())
	stdout, stderr, runErr := runCommand(ctx, cmd)
	if runErr != nil {
		if _, _, parseErr := parseAntigravity(stdout, agent.Model); parseErr != nil && !errors.Is(parseErr, ErrInvalidOutput) {
			return nil, Metadata{}, stdout, stderr, parseErr
		}
		return nil, Metadata{}, stdout, stderr, classifyFailure(stdout, stderr, runErr)
	}
	review, metadata, err := parseAntigravity(stdout, agent.Model)
	if err != nil {
		return nil, Metadata{}, stdout, stderr, err
	}
	return review, metadata, stdout, stderr, nil
}

type antigravityResult struct {
	Status     string          `json:"status"`
	Response   string          `json:"response"`
	Error      string          `json:"error"`
	Structured json.RawMessage `json:"structured_output"`
}

func parseAntigravity(raw []byte, model string) (*runstore.Review, Metadata, error) {
	var result antigravityResult
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64*1024), outputLimit)
	resultSeen := false
	for scanner.Scan() {
		var event struct {
			Event  string            `json:"event"`
			Result antigravityResult `json:"result"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, Metadata{}, fmt.Errorf("%w: invalid Antigravity JSONL event: %v", ErrInvalidOutput, err)
		}
		if event.Event == "result" {
			if resultSeen {
				return nil, Metadata{}, fmt.Errorf("%w: Antigravity returned multiple results", ErrInvalidOutput)
			}
			result, resultSeen = event.Result, true
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, Metadata{}, fmt.Errorf("%w: Antigravity JSONL: %v", ErrInvalidOutput, err)
	}
	if !resultSeen {
		return nil, Metadata{}, fmt.Errorf("%w: Antigravity returned no result", ErrInvalidOutput)
	}
	if result.Status != "SUCCESS" {
		message := result.Error
		if message == "" {
			message = result.Status
		}
		return nil, Metadata{}, classifyMessage("Antigravity: "+message, ErrAgentFailed)
	}
	payload := result.Structured
	if len(payload) == 0 || bytes.Equal(payload, []byte("null")) {
		return nil, Metadata{}, fmt.Errorf("%w: Antigravity returned no structured result", ErrInvalidOutput)
	}
	if err := config.Validate("schema/review-result.json", payload); err != nil {
		return nil, Metadata{}, fmt.Errorf("%w: invalid Antigravity review: %v", ErrInvalidOutput, err)
	}
	var review runstore.Review
	if err := json.Unmarshal(payload, &review); err != nil {
		return nil, Metadata{}, err
	}
	if model == "" {
		model = "unknown"
	}
	return &review, Metadata{Model: model}, nil
}
