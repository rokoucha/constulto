package reviewer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rokoucha/constulto/assets"
	"github.com/rokoucha/constulto/internal/config"
	"github.com/rokoucha/constulto/internal/runstore"
)

func probeMuse(ctx context.Context, command string) Probe {
	version, err := exec.CommandContext(ctx, command, "--version").CombinedOutput()
	if err != nil {
		return Probe{Error: err.Error()}
	}
	help, err := exec.CommandContext(ctx, command, "exec", "--help").CombinedOutput()
	if err != nil {
		return Probe{Version: strings.TrimSpace(string(version)), Error: err.Error()}
	}
	for _, flag := range []string{"--json", "--prompt-file", "--workspace", "--output-schema", "--disable-web-tools", "--no-foreign-personal-context", "--no-session-log", "--approval-mode", "--approval-judge", "--disable-write", "--disable-shell"} {
		if !bytes.Contains(help, []byte(flag)) {
			return Probe{Version: strings.TrimSpace(string(version)), Error: "required capability missing: " + flag}
		}
	}
	return Probe{Version: strings.TrimSpace(string(version)), Supported: true}
}

func runMuse(ctx context.Context, agent config.Agent, workingDir string, prompt []byte) (*runstore.Review, Metadata, []byte, []byte, error) {
	tmp, err := os.MkdirTemp("", "constulto-muse-")
	if err != nil {
		return nil, Metadata{}, nil, nil, err
	}
	defer os.RemoveAll(tmp)
	_ = os.Chmod(tmp, 0700)
	promptPath, schemaPath := filepath.Join(tmp, "prompt.md"), filepath.Join(tmp, "schema.json")
	if err = os.WriteFile(promptPath, prompt, 0600); err != nil {
		return nil, Metadata{}, nil, nil, err
	}
	schema, err := assets.FS.ReadFile("schema/review-result.json")
	if err != nil {
		return nil, Metadata{}, nil, nil, err
	}
	schema, err = claudeSchema(schema)
	if err != nil {
		return nil, Metadata{}, nil, nil, err
	}
	if err = os.WriteFile(schemaPath, schema, 0600); err != nil {
		return nil, Metadata{}, nil, nil, err
	}
	args := []string{"exec", "--json", "--prompt-file", promptPath, "--workspace", workingDir, "--worktree", "off", "--output-schema", schemaPath, "--disable-web-tools", "--no-foreign-personal-context", "--no-session-log", "--approval-mode", "never", "--approval-judge", "off", "--disable-write", "--disable-shell", "--no-parallel-tool-calls"}
	if agent.Model != "" {
		args = append(args, "--model", agent.Model)
	}
	cmd := exec.Command(agent.Command, args...)
	cmd.Dir = workingDir
	cmd.Env = childEnv(os.Environ())
	stdout, stderr, runErr := runCommand(ctx, cmd)
	if runErr != nil {
		return nil, Metadata{}, stdout, stderr, classifyFailure(stdout, stderr, runErr)
	}
	review, err := parseMuse(stdout)
	if err != nil {
		return nil, Metadata{}, stdout, stderr, err
	}
	return review, Metadata{}, stdout, stderr, nil
}

func parseMuse(raw []byte) (*runstore.Review, error) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64*1024), outputLimit)
	var final string
	terminalSeen := false
	for scanner.Scan() {
		var event struct {
			PayloadType string `json:"payload_type"`
			Payload     struct {
				Terminal string `json:"terminal"`
				Text     string `json:"text"`
				Reason   any    `json:"reason"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("%w: invalid Muse JSONL event: %v", ErrInvalidOutput, err)
		}
		if strings.HasPrefix(event.PayloadType, "run.terminal.") {
			if terminalSeen {
				return nil, fmt.Errorf("%w: Muse returned multiple terminal events", ErrInvalidOutput)
			}
			terminalSeen = true
			if event.PayloadType != "run.terminal.completed" || event.Payload.Terminal != "completed" {
				reason, _ := json.Marshal(event.Payload.Reason)
				return nil, classifyMessage("Muse run failed: "+string(reason), ErrAgentFailed)
			}
			final = event.Payload.Text
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%w: Muse JSONL: %v", ErrInvalidOutput, err)
	}
	if !terminalSeen || final == "" {
		return nil, fmt.Errorf("%w: Muse returned no terminal result", ErrInvalidOutput)
	}
	if err := config.Validate("schema/review-result.json", []byte(final)); err != nil {
		return nil, fmt.Errorf("%w: invalid Muse review: %v", ErrInvalidOutput, err)
	}
	var review runstore.Review
	if err := json.Unmarshal([]byte(final), &review); err != nil {
		return nil, err
	}
	return &review, nil
}
