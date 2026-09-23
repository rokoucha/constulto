package reviewer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rokoucha/constulto/internal/config"
)

// TestProbeCodex は正常なCodex CLI出力に対してProbeが成功することを検証します。
func TestProbeCodex(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "fake-codex")
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "WARNING: proceeding without alias"
  echo "codex-cli 0.156.1"
elif [ "$1" = "--help" ]; then
  echo "--no-daemon --ask-for-approval exec"
elif [ "$1" = "exec" ] && [ "$2" = "--help" ]; then
  echo "--json --output-schema --sandbox --ephemeral --ignore-user-config --ignore-rules --cd --model"
fi
`
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}

	probe := probeCodex(context.Background(), command)
	if !probe.Supported {
		t.Fatalf("expected supported probe, got error: %s", probe.Error)
	}
	if probe.Version != "codex-cli 0.156.1" {
		t.Fatalf("unexpected version: %q", probe.Version)
	}
}

// TestProbeCodexRejectsMissingFlags は必須フラグが欠けている場合にProbeが失敗することを検証します。
func TestProbeCodexRejectsMissingFlags(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "fake-codex")
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "codex-cli 0.156.1"
elif [ "$1" = "--help" ]; then
  echo "--no-daemon exec"
elif [ "$1" = "exec" ] && [ "$2" = "--help" ]; then
  echo "--json --output-schema --sandbox --ephemeral --ignore-user-config --ignore-rules --cd --model"
fi
`
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}

	probe := probeCodex(context.Background(), command)
	if probe.Supported || !strings.Contains(probe.Error, "--ask-for-approval") {
		t.Fatalf("unexpected probe result: %#v", probe)
	}
}

// TestParseCodexCompletedReview はCodexのJSONL出力から構造化レビューとメタデータを正常に解析できることを検証します。
func TestParseCodexCompletedReview(t *testing.T) {
	raw := []byte(`{"type":"thread.started","model":"gpt-5"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"{\"summary\":\"all good\",\"findings\":[],\"decisions\":[],\"scope\":{\"reviewed\":[\"file.go\"],\"notReviewed\":[],\"testsNotRun\":[]}}","model":"gpt-5"}}
{"type":"turn.completed"}
`)
	review, metadata, err := parseCodex(raw, "gpt-5")
	if err != nil {
		t.Fatal(err)
	}
	if review.Summary != "all good" || len(review.Scope.Reviewed) != 1 {
		t.Fatalf("unexpected review: %#v", review)
	}
	if metadata.Model != "gpt-5" || len(metadata.ObservedModels) != 1 || metadata.ObservedModels[0] != "gpt-5" {
		t.Fatalf("unexpected metadata: %#v", metadata)
	}
}

// TestParseCodexHandlesMarkdownFences はMarkdownコードフェンスで囲まれたJSONも適切にパースできることを検証します。
func TestParseCodexHandlesMarkdownFences(t *testing.T) {
	raw := []byte(`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"` + "```json\\n{\\\"summary\\\":\\\"fenced\\\",\\\"findings\\\":[],\\\"decisions\\\":[],\\\"scope\\\":{\\\"reviewed\\\":[],\\\"notReviewed\\\":[],\\\"testsNotRun\\\":[]}}\\n```" + `"}}
{"type":"turn.completed"}
`)
	review, metadata, err := parseCodex(raw, "default-model")
	if err != nil {
		t.Fatal(err)
	}
	if review.Summary != "fenced" {
		t.Fatalf("unexpected summary: %s", review.Summary)
	}
	if metadata.Model != "default-model" {
		t.Fatalf("unexpected model: %s", metadata.Model)
	}
}

// TestParseCodexErrors は各種エラーイベントおよび無効な出力に対する振る舞いを検証します。
func TestParseCodexErrors(t *testing.T) {
	// エラーイベント
	errorJSONL := []byte(`{"type":"error","message":"rate limit exceeded"}` + "\n")
	_, _, err := parseCodex(errorJSONL, "")
	if !errors.Is(err, ErrRateLimit) {
		t.Fatalf("expected rate limit error, got: %v", err)
	}

	// ターン失敗イベント
	failedJSONL := []byte(`{"type":"turn.failed","message":"permission denied to workspace"}` + "\n")
	_, _, err = parseCodex(failedJSONL, "")
	if !errors.Is(err, ErrPermission) {
		t.Fatalf("expected permission error, got: %v", err)
	}

	// 不正なJSONL
	_, _, err = parseCodex([]byte("invalid json line\n"), "")
	if !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("expected invalid output error, got: %v", err)
	}

	// エージェントメッセージなし
	_, _, err = parseCodex([]byte(`{"type":"turn.completed"}`+"\n"), "")
	if !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("expected invalid output error for missing message, got: %v", err)
	}

	// スキーマ不適合
	invalidSchema := []byte(`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"{\"invalid\":\"schema\"}"}}` + "\n")
	_, _, err = parseCodex(invalidSchema, "")
	if !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("expected schema validation error, got: %v", err)
	}
}

// TestRunCodexUsesRestrictedEnvironment はCodex実行時に安全制御フラグや引数が正しく渡されることを検証します。
func TestRunCodexUsesRestrictedEnvironment(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "fake-codex")

	script := `#!/bin/sh
# 必須の安全制御引数が渡されているか検証
args="$*"
case "$args" in
  *"--no-daemon"*"--ask-for-approval never"*"exec"*"--json"*"--output-schema"*"--sandbox read-only"*"--ephemeral"*"--ignore-user-config"*"--ignore-rules"*"--color never"*"--cd "*"--model test-model"*" -")
    ;;
  *)
    echo "unexpected args: $args" >&2
    exit 2
    ;;
esac

# stdinからプロンプトが届いているか確認
read -r stdin_content
if [ "$stdin_content" != "test prompt" ]; then
  echo "unexpected stdin: $stdin_content" >&2
  exit 3
fi

cat <<'EOF'
{"type":"thread.started","model":"test-model"}
{"type":"item.completed","item":{"id":"1","type":"agent_message","text":"{\"summary\":\"verified\",\"findings\":[],\"decisions\":[],\"scope\":{\"reviewed\":[],\"notReviewed\":[],\"testsNotRun\":[]}}","model":"test-model"}}
{"type":"turn.completed"}
EOF
`

	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	agent := config.Agent{
		Adapter: "codex",
		Command: command,
		Model:   "test-model",
	}

	result, metadata, stdout, stderr, err := Run(ctx, agent, dir, []byte("test prompt"))
	if err != nil {
		t.Fatalf("Run failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	if result.Summary != "verified" {
		t.Fatalf("unexpected summary: %s", result.Summary)
	}
	if metadata.Model != "test-model" {
		t.Fatalf("unexpected metadata model: %s", metadata.Model)
	}

	restrictions := Restrictions(agent.Adapter)
	expectedRestrictions := []string{
		"sandbox:read-only", "approval:never", "session:ephemeral", "user-config:ignored", "rules:ignored", "daemon:disabled",
	}
	if len(restrictions) != len(expectedRestrictions) {
		t.Fatalf("unexpected restrictions count: %#v", restrictions)
	}
	for i, r := range expectedRestrictions {
		if restrictions[i] != r {
			t.Fatalf("restriction[%d]: got %q, want %q", i, restrictions[i], r)
		}
	}
}
