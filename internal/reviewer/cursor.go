package reviewer

import (
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

func probeCursor(ctx context.Context, command string) Probe {
	version, err := exec.CommandContext(ctx, command, "--version").CombinedOutput()
	if err != nil {
		return Probe{Error: strings.TrimSpace(string(version)) + ": " + err.Error()}
	}
	versionText := strings.TrimSpace(string(version))
	help, err := exec.CommandContext(ctx, command, "--help").CombinedOutput()
	if err != nil {
		return Probe{Version: versionText, Error: err.Error()}
	}
	for _, flag := range []string{"--print", "--output-format", "--mode", "--sandbox", "--workspace", "--model", "--trust"} {
		if !bytes.Contains(help, []byte(flag)) {
			return Probe{Version: versionText, Error: "required capability missing: " + flag}
		}
	}
	return Probe{Version: versionText, Supported: true}
}

func runCursor(ctx context.Context, agent config.Agent, workingDir string, prompt []byte) (*runstore.Review, Metadata, []byte, []byte, error) {
	configDir, err := cursorConfigDir()
	if err != nil {
		return nil, Metadata{}, nil, nil, err
	}
	defer os.RemoveAll(configDir)
	workspace, err := cursorWorkspace(configDir, workingDir)
	if err != nil {
		return nil, Metadata{}, nil, nil, err
	}
	schema, err := assets.FS.ReadFile("schema/review-result.json")
	if err != nil {
		return nil, Metadata{}, nil, nil, err
	}
	input := make([]byte, 0, len(prompt)+len(schema)+128)
	input = append(input, prompt...)
	input = append(input, []byte("\n\nReturn only a JSON object matching this schema, with no Markdown fences:\n")...)
	input = append(input, schema...)
	input = append(input, '\n')

	args := []string{"--print", "--output-format", "json", "--mode", "ask", "--sandbox", "enabled", "--trust", "--workspace", workspace}
	if agent.Model != "" {
		args = append(args, "--model", agent.Model)
	}
	cmd := exec.Command(agent.Command, args...)
	cmd.Dir = workspace
	cmd.Stdin = bytes.NewReader(input)
	cmd.Env = replaceEnv(childEnv(os.Environ()), map[string]string{"CURSOR_CONFIG_DIR": configDir})
	stdout, stderr, runErr := runCommand(ctx, cmd)
	if runErr != nil {
		return nil, Metadata{}, stdout, stderr, classifyFailure(stdout, stderr, runErr)
	}
	review, err := parseCursor(stdout)
	if err != nil {
		return nil, Metadata{}, stdout, stderr, err
	}
	return review, Metadata{Model: agent.Model}, stdout, stderr, nil
}

func cursorConfigDir() (string, error) {
	dir, err := os.MkdirTemp("", "constulto-cursor-")
	if err != nil {
		return "", err
	}
	config := []byte(`{"version":1,"editor":{"vimMode":false},"permissions":{"allow":[],"deny":["Shell(*)","Write(*)","WebFetch(*)","Mcp(*:*)"]}}`)
	if err := os.WriteFile(filepath.Join(dir, "cli-config.json"), config, 0600); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	hookPath := filepath.Join(dir, "deny-subagent.sh")
	hook := []byte("#!/bin/sh\nprintf '%s\\n' '{\"permission\":\"deny\",\"agent_message\":\"Subagent delegation is disabled for this review.\"}'\n")
	if err := os.WriteFile(hookPath, hook, 0700); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	hooks, err := json.Marshal(map[string]any{"version": 1, "hooks": map[string]any{
		"preToolUse":    []map[string]string{{"command": hookPath, "matcher": "Task"}},
		"subagentStart": []map[string]string{{"command": hookPath}},
	}})
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(dir, "workspace", ".cursor"), 0700); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "workspace", ".cursor", "hooks.json"), hooks, 0600); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

func cursorWorkspace(configDir, workingDir string) (string, error) {
	workspace := filepath.Join(configDir, "workspace")
	entries, err := os.ReadDir(workingDir)
	if err != nil {
		return "", err
	}
	excluded := map[string]bool{".cursor": true, "AGENTS.md": true, "CLAUDE.md": true, ".agents": true}
	for _, entry := range entries {
		if excluded[entry.Name()] {
			continue
		}
		if err := os.Symlink(filepath.Join(workingDir, entry.Name()), filepath.Join(workspace, entry.Name())); err != nil {
			return "", fmt.Errorf("create isolated Cursor workspace: %w", err)
		}
	}
	return workspace, nil
}

func parseCursor(raw []byte) (*runstore.Review, error) {
	var envelope struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		IsError bool   `json:"is_error"`
		Result  string `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("%w: invalid Cursor JSON result: %v", ErrInvalidOutput, err)
	}
	if envelope.Type != "result" || envelope.Subtype != "success" || envelope.IsError {
		if envelope.IsError || envelope.Subtype == "error" {
			return nil, classifyMessage("Cursor: "+envelope.Result, ErrAgentFailed)
		}
		return nil, fmt.Errorf("%w: Cursor returned no successful result", ErrInvalidOutput)
	}
	result := stripJSONFence(envelope.Result)
	if !json.Valid([]byte(result)) {
		if start := strings.IndexByte(result, '{'); start >= 0 {
			result = result[start:]
		}
	}
	payload := []byte(result)
	if err := config.Validate("schema/review-result.json", payload); err != nil {
		return nil, fmt.Errorf("%w: invalid Cursor review: %v", ErrInvalidOutput, err)
	}
	var review runstore.Review
	if err := json.Unmarshal(payload, &review); err != nil {
		return nil, fmt.Errorf("%w: invalid Cursor review: %v", ErrInvalidOutput, err)
	}
	return &review, nil
}
