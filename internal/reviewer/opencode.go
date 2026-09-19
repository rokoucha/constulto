package reviewer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/rokoucha/constulto/assets"
	"github.com/rokoucha/constulto/internal/config"
	"github.com/rokoucha/constulto/internal/runstore"
)

const openCodeAgent = "constulto-reviewer"

func probeOpenCode(ctx context.Context, command string) Probe {
	version, err := exec.CommandContext(ctx, command, "--version").CombinedOutput()
	if err != nil {
		return Probe{Error: strings.TrimSpace(string(version)) + ": " + err.Error()}
	}
	versionText := strings.TrimSpace(string(version))
	if !regexp.MustCompile(`(^|[^0-9])1\.18\.[0-9]+([^0-9]|$)`).MatchString(versionText) {
		return Probe{Version: versionText, Error: "unsupported OpenCode version; constulto requires 1.18.x"}
	}
	help, err := exec.CommandContext(ctx, command, "run", "--help").CombinedOutput()
	if err != nil {
		return Probe{Version: versionText, Error: err.Error()}
	}
	for _, flag := range []string{"--pure", "--format", "--agent", "--dir", "--model"} {
		if !bytes.Contains(help, []byte(flag)) {
			return Probe{Version: versionText, Error: "required capability missing: " + flag}
		}
	}
	return Probe{Version: versionText, Supported: true}
}

func runOpenCode(ctx context.Context, agent config.Agent, workingDir string, prompt []byte) (*runstore.Review, Metadata, []byte, []byte, error) {
	configDir, err := os.MkdirTemp("", "constulto-opencode-config-")
	if err != nil {
		return nil, Metadata{}, nil, nil, err
	}
	defer os.RemoveAll(configDir)
	_ = os.Chmod(configDir, 0700)
	workspace, err := openCodeWorkspace(configDir, workingDir)
	if err != nil {
		return nil, Metadata{}, nil, nil, err
	}

	content, permissions, err := openCodeConfig(agent.Model)
	if err != nil {
		return nil, Metadata{}, nil, nil, err
	}
	args := []string{"run", "--pure", "--format", "json", "--agent", openCodeAgent, "--dir", workspace}
	if agent.Model != "" {
		args = append(args, "--model", agent.Model)
	}
	cmd := exec.Command(agent.Command, args...)
	// Keep configuration discovery away from the untrusted repository. --dir
	// still gives the reviewer read/search access to the intended workspace.
	cmd.Dir = configDir
	schema, err := assets.FS.ReadFile("schema/review-result.json")
	if err != nil {
		return nil, Metadata{}, nil, nil, err
	}
	var input bytes.Buffer
	input.Write(bytes.ReplaceAll(prompt, []byte(workingDir), []byte(workspace)))
	input.WriteString("\n\nReturn only one JSON object matching this schema exactly. Do not use Markdown fences or add prose.\n<output-schema>\n")
	input.Write(schema)
	input.WriteString("\n</output-schema>\n")
	cmd.Stdin = &input
	cmd.Env = replaceEnv(os.Environ(), map[string]string{
		"CONSTULTO_DEPTH":                     "1",
		"XDG_CONFIG_HOME":                     configDir,
		"OPENCODE_CONFIG":                     "",
		"OPENCODE_CONFIG_DIR":                 configDir,
		"OPENCODE_CONFIG_CONTENT":             string(content),
		"OPENCODE_PERMISSION":                 string(permissions),
		"OPENCODE_AUTO_SHARE":                 "false",
		"OPENCODE_DISABLE_DEFAULT_PLUGINS":    "true",
		"OPENCODE_DISABLE_EXTERNAL_SKILLS":    "true",
		"OPENCODE_DISABLE_PROJECT_CONFIG":     "true",
		"OPENCODE_DISABLE_CLAUDE_CODE":        "true",
		"OPENCODE_DISABLE_CLAUDE_CODE_PROMPT": "true",
		"OPENCODE_DISABLE_CLAUDE_CODE_SKILLS": "true",
		"OPENCODE_ENABLE_EXA":                 "false",
		"OPENCODE_ENABLE_PARALLEL":            "false",
		"OPENCODE_PURE":                       "true",
	})
	stdout, stderr, runErr := runCommand(ctx, cmd)
	if runErr != nil {
		return nil, Metadata{}, stdout, stderr, classifyFailure(stdout, stderr, runErr)
	}
	review, metadata, err := parseOpenCode(stdout)
	if err != nil {
		return nil, Metadata{}, stdout, stderr, err
	}
	metadata.Model = "unknown"
	return review, metadata, stdout, stderr, nil
}

func openCodeWorkspace(configDir, workingDir string) (string, error) {
	workspace := filepath.Join(configDir, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(workingDir)
	if err != nil {
		return "", err
	}
	excluded := map[string]bool{
		"opencode.json": true, "opencode.jsonc": true, ".opencode": true, ".git": true,
		"AGENTS.md": true, "CLAUDE.md": true, "CONTEXT.md": true,
		".claude": true, ".agents": true,
	}
	for _, entry := range entries {
		if excluded[entry.Name()] {
			continue
		}
		if err := os.Symlink(filepath.Join(workingDir, entry.Name()), filepath.Join(workspace, entry.Name())); err != nil {
			return "", fmt.Errorf("create isolated OpenCode workspace: %w", err)
		}
	}
	return workspace, nil
}

func openCodeConfig(model string) ([]byte, []byte, error) {
	read := map[string]string{"*": "allow", "*.env": "deny", "*.env.*": "deny", "*.env.example": "allow"}
	permission := map[string]any{
		"*": "deny", "read": read, "glob": "allow", "grep": "allow", "list": "allow",
		"edit": "deny", "bash": "deny", "task": "deny", "external_directory": "deny",
		"webfetch": "deny", "websearch": "deny", "skill": "deny", "lsp": "deny",
		"question": "deny", "todowrite": "deny",
	}
	reviewerAgent := map[string]any{
		"description": "Read-only independent reviewer", "mode": "primary", "permission": permission,
		"prompt": "Review only. Never modify files, run shell commands, access the network, or delegate work.",
	}
	if model != "" {
		reviewerAgent["model"] = model
	}
	document := map[string]any{
		"share":      "disabled",
		"permission": map[string]string{"*": "deny"},
		"agent":      map[string]any{openCodeAgent: reviewerAgent},
		"mcp":        map[string]any{}, "plugin": []string{}, "instructions": []string{},
	}
	content, err := json.Marshal(document)
	if err != nil {
		return nil, nil, err
	}
	permissions, err := json.Marshal(permission)
	return content, permissions, err
}

func parseOpenCode(raw []byte) (*runstore.Review, Metadata, error) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64*1024), outputLimit)
	var answer strings.Builder
	var cost float64
	hasCost := false
	finished := false
	for scanner.Scan() {
		var event struct {
			Type  string `json:"type"`
			Error any    `json:"error"`
			Part  struct {
				Text   string   `json:"text"`
				Reason string   `json:"reason"`
				Cost   *float64 `json:"cost"`
			} `json:"part"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, Metadata{}, fmt.Errorf("%w: invalid OpenCode JSONL event: %v", ErrInvalidOutput, err)
		}
		switch event.Type {
		case "error":
			message, _ := json.Marshal(event.Error)
			return nil, Metadata{}, classifyMessage("OpenCode error: "+string(message), ErrAgentFailed)
		case "step_start":
			answer.Reset()
		case "text":
			answer.WriteString(event.Part.Text)
		case "step_finish":
			if event.Part.Reason != "stop" && event.Part.Reason != "tool-calls" {
				return nil, Metadata{}, classifyMessage("OpenCode finished with reason "+event.Part.Reason, ErrAgentFailed)
			}
			if event.Part.Reason == "stop" {
				finished = true
			}
			if event.Part.Cost != nil && *event.Part.Cost >= 0 && !math.IsNaN(*event.Part.Cost) && !math.IsInf(*event.Part.Cost, 0) {
				cost += *event.Part.Cost
				hasCost = true
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, Metadata{}, fmt.Errorf("%w: OpenCode JSONL: %v", ErrInvalidOutput, err)
	}
	if !finished || answer.Len() == 0 {
		return nil, Metadata{}, fmt.Errorf("%w: OpenCode returned no completed result", ErrInvalidOutput)
	}
	payload := []byte(stripJSONFence(answer.String()))
	if err := config.Validate("schema/review-result.json", payload); err != nil {
		return nil, Metadata{}, fmt.Errorf("%w: invalid OpenCode review: %v", ErrInvalidOutput, err)
	}
	var review runstore.Review
	if err := json.Unmarshal(payload, &review); err != nil {
		return nil, Metadata{}, err
	}
	metadata := Metadata{}
	if hasCost {
		metadata.ProviderCostUSD = &cost
	}
	return &review, metadata, nil
}

func stripJSONFence(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "```json") && strings.HasSuffix(value, "```") {
		return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(value, "```json"), "```"))
	}
	if strings.HasPrefix(value, "```") && strings.HasSuffix(value, "```") {
		return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(value, "```"), "```"))
	}
	return value
}
