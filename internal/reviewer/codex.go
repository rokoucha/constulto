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
	"sort"
	"strings"

	"github.com/rokoucha/constulto/assets"
	"github.com/rokoucha/constulto/internal/config"
	"github.com/rokoucha/constulto/internal/runstore"
)

// cleanVersion はCLI出力から警告行を除去し、バージョン文字列を抽出します。
func cleanVersion(raw []byte) string {
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" && !strings.HasPrefix(line, "WARNING:") {
			return line
		}
	}
	return strings.TrimSpace(string(raw))
}

// probeCodex はCodex CLIのバージョンと必要な機能フラグを検査します。
func probeCodex(ctx context.Context, command string) Probe {
	version, err := exec.CommandContext(ctx, command, "--version").CombinedOutput()
	if err != nil {
		return Probe{Error: cleanVersion(version) + ": " + err.Error()}
	}
	versionText := cleanVersion(version)

	help, err := exec.CommandContext(ctx, command, "--help").CombinedOutput()
	if err != nil {
		return Probe{Version: versionText, Error: err.Error()}
	}
	for _, flag := range []string{"--no-daemon", "--ask-for-approval", "exec"} {
		if !bytes.Contains(help, []byte(flag)) {
			return Probe{Version: versionText, Error: "required capability missing: " + flag}
		}
	}

	execHelp, err := exec.CommandContext(ctx, command, "exec", "--help").CombinedOutput()
	if err != nil {
		return Probe{Version: versionText, Error: err.Error()}
	}
	for _, flag := range []string{"--json", "--output-schema", "--sandbox", "--ephemeral", "--ignore-user-config", "--ignore-rules", "--cd", "--model"} {
		if !bytes.Contains(execHelp, []byte(flag)) {
			return Probe{Version: versionText, Error: "required capability missing: " + flag}
		}
	}

	return Probe{Version: versionText, Supported: true}
}

// runCodex はCodex CLIを隔離された環境で実行し、構造化レビュー結果を取得します。
func runCodex(ctx context.Context, agent config.Agent, workingDir string, prompt []byte) (*runstore.Review, Metadata, []byte, []byte, error) {
	tmp, err := os.MkdirTemp("", "constulto-codex-")
	if err != nil {
		return nil, Metadata{}, nil, nil, err
	}
	defer os.RemoveAll(tmp)
	_ = os.Chmod(tmp, 0700)

	schemaPath := filepath.Join(tmp, "schema.json")
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

	args := []string{
		"--no-daemon",
		"--ask-for-approval", "never",
		"exec",
		"--json",
		"--output-schema", schemaPath,
		"--sandbox", "read-only",
		"--ephemeral",
		"--ignore-user-config",
		"--ignore-rules",
		"--color", "never",
		"--cd", workingDir,
	}
	if agent.Model != "" {
		args = append(args, "--model", agent.Model)
	}
	args = append(args, "-")

	cmd := exec.Command(agent.Command, args...)
	cmd.Dir = workingDir
	cmd.Stdin = bytes.NewReader(prompt)
	cmd.Env = childEnv(os.Environ())

	stdout, stderr, runErr := runCommand(ctx, cmd)
	if runErr != nil {
		return nil, Metadata{}, stdout, stderr, classifyFailure(stdout, stderr, runErr)
	}

	review, metadata, err := parseCodex(stdout, agent.Model)
	if err != nil {
		return nil, Metadata{}, stdout, stderr, err
	}
	return review, metadata, stdout, stderr, nil
}

// codexEvent はCodex CLIの--json出力のJSONLイベントを表します。
type codexEvent struct {
	Type    string          `json:"type"`
	Error   any             `json:"error,omitempty"`
	Message string          `json:"message,omitempty"`
	Item    *codexItem      `json:"item,omitempty"`
	Model   string          `json:"model,omitempty"`
}

type codexItem struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Text  string `json:"text,omitempty"`
	Model string `json:"model,omitempty"`
}

// parseCodex はCodex CLIのJSONL出力を解析し、構造化レビューとメタデータを抽出します。
func parseCodex(raw []byte, defaultModel string) (*runstore.Review, Metadata, error) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64*1024), outputLimit)

	var finalText string
	var observed []string
	seenModels := make(map[string]bool)
	terminalSeen := false

	addModel := func(m string) {
		m = strings.TrimSpace(m)
		if m != "" && !seenModels[m] {
			seenModels[m] = true
			observed = append(observed, m)
		}
	}

	for scanner.Scan() {
		var event codexEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, Metadata{}, fmt.Errorf("%w: invalid Codex JSONL event: %v", ErrInvalidOutput, err)
		}

		if event.Model != "" {
			addModel(event.Model)
		}

		switch event.Type {
		case "error":
			msg := event.Message
			if msg == "" && event.Error != nil {
				b, _ := json.Marshal(event.Error)
				msg = string(b)
			}
			return nil, Metadata{}, classifyMessage("Codex error: "+msg, ErrAgentFailed)
		case "turn.failed":
			msg := event.Message
			if msg == "" && event.Error != nil {
				b, _ := json.Marshal(event.Error)
				msg = string(b)
			}
			return nil, Metadata{}, classifyMessage("Codex turn failed: "+msg, ErrAgentFailed)
		case "item.completed":
			if event.Item != nil {
				if event.Item.Model != "" {
					addModel(event.Item.Model)
				}
				if event.Item.Type == "agent_message" && event.Item.Text != "" {
					finalText = event.Item.Text
				}
			}
		case "turn.completed":
			terminalSeen = true
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, Metadata{}, fmt.Errorf("%w: Codex JSONL: %v", ErrInvalidOutput, err)
	}

	if finalText == "" {
		return nil, Metadata{}, fmt.Errorf("%w: Codex returned no agent message", ErrInvalidOutput)
	}

	payload := []byte(stripJSONFence(finalText))
	if err := config.Validate("schema/review-result.json", payload); err != nil {
		return nil, Metadata{}, fmt.Errorf("%w: invalid Codex review: %v", ErrInvalidOutput, err)
	}

	var review runstore.Review
	if err := json.Unmarshal(payload, &review); err != nil {
		return nil, Metadata{}, err
	}

	sort.Strings(observed)
	model := defaultModel
	if len(observed) > 0 {
		model = observed[0]
	}
	if model == "" {
		model = "unknown"
	}

	metadata := Metadata{
		Model:          model,
		ObservedModels: observed,
	}

	_ = terminalSeen // ターン完了フラグ（将来の拡張用）

	return &review, metadata, nil
}
