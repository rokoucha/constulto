package reviewer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rokoucha/constulto/internal/config"
)

const validReview = `{"summary":"checked","findings":[],"decisions":[],"scope":{"reviewed":["brief"],"notReviewed":["runtime"],"testsNotRun":["integration"]}}`

func TestRunParsesStructuredOutput(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "fake-claude")
	script := "#!/bin/sh\nprintf '%s' '" + `{"structured_output":` + validReview + `,"total_cost_usd":0.25,"modelUsage":{"model-a":{"outputTokens":3},"model-b":{"outputTokens":9}}}` + "'\n"
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, metadata, _, _, err := Run(ctx, config.Agent{Adapter: "claude", Command: command}, dir, []byte("prompt"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary != "checked" || len(result.Scope.NotReviewed) != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if metadata.Model != "model-b" || len(metadata.ObservedModels) != 2 || metadata.ProviderCostUSD == nil || *metadata.ProviderCostUSD != 0.25 {
		t.Fatalf("unexpected metadata: %#v", metadata)
	}
}

func TestNativeFailureKinds(t *testing.T) {
	tests := []struct {
		message string
		target  error
		kind    string
	}{{"permission denied", ErrPermission, "permission"}, {"usage limit reached", ErrRateLimit, "rate-limit"}}
	for _, tt := range tests {
		err := nativeFailure([]byte(`{"result":"`+tt.message+`"}`), errors.New("exit"))
		if !errors.Is(err, tt.target) || ErrorKind(err) != tt.kind {
			t.Fatalf("%q: %v %s", tt.message, err, ErrorKind(err))
		}
	}
}

func TestClassifyFailureIgnoresStdoutAndBoundsDetail(t *testing.T) {
	err := classifyFailure([]byte("authentication failed"), []byte(strings.Repeat("x", 5000)), errors.New("exit 1"))
	if errors.Is(err, ErrAuthentication) || len(err.Error()) > 4200 {
		t.Fatalf("unexpected classification: %v", err)
	}
}

func TestParseClaudeClassifiesExitZeroError(t *testing.T) {
	_, _, err := parseClaude([]byte(`{"is_error":true,"result":"OAuth login expired"}`))
	if !errors.Is(err, ErrAuthentication) {
		t.Fatalf("expected authentication error, got %v", err)
	}
}

func TestParseClaudeOmitsNegativeCost(t *testing.T) {
	review, metadata, err := parseClaude([]byte(`{"structured_output":` + validReview + `,"total_cost_usd":-1}`))
	if err != nil || review == nil || metadata.ProviderCostUSD != nil {
		t.Fatalf("unexpected result: %#v %#v %v", review, metadata, err)
	}
}

func TestLimitedBufferMarksOverflow(t *testing.T) {
	b := &limitedBuffer{limit: 3}
	n, err := b.Write([]byte("12345"))
	if err != nil || n != 5 || !b.overflow || string(b.Bytes()) != "123" {
		t.Fatalf("n=%d err=%v overflow=%v bytes=%q", n, err, b.overflow, b.Bytes())
	}
}

func TestRunStopsProcessWhenOutputLimitIsExceeded(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "fake-claude")
	script := "#!/bin/sh\nyes x | head -c 17000000\nsleep 60\n"
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started := time.Now()
	_, _, _, _, err := Run(ctx, config.Agent{Adapter: "claude", Command: command}, dir, []byte("prompt"))
	if !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("expected output-limit error, got %v", err)
	}
	if time.Since(started) > 4*time.Second {
		t.Fatal("output limit did not stop the process promptly")
	}
}

func TestClaudeSchemaRemovesRemoteMetadata(t *testing.T) {
	got, err := claudeSchema([]byte(`{"$schema":"remote","$id":"id","type":"object"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "$schema") || strings.Contains(string(got), "$id") {
		t.Fatalf("metadata remains: %s", got)
	}
}

func TestProbeRejectsMissingInvocationFlag(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "fake-claude")
	script := `#!/bin/sh
case "$1" in
  --version) echo 1.0 ;;
  --help) echo '--print --safe-mode --restricted --strict-mcp-config --mcp-config --permission-mode --permission-prompts --disable-slash-commands --no-session-persistence --output-format --json-schema --model' ;;
esac
`
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	probe := ProbeClaude(context.Background(), command)
	if probe.Supported || !strings.Contains(probe.Error, "--tools") {
		t.Fatalf("unexpected probe: %#v", probe)
	}
}

func TestRunRejectsInvalidStructuredOutput(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "fake-claude")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nprintf '%s' '{\"structured_output\":{\"summary\":\"missing fields\"}}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, _, _, err := Run(ctx, config.Agent{Adapter: "claude", Command: command}, dir, []byte("prompt"))
	if err == nil {
		t.Fatal("expected invalid output error")
	}
}

func TestRunClassifiesNativeAuthenticationFailure(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "fake-claude")
	script := `#!/bin/sh
printf '%s' '{"is_error":true,"result":"OAuth session expired and could not be refreshed"}'
exit 1
`
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, _, _, err := Run(ctx, config.Agent{Adapter: "claude", Command: command}, dir, []byte("prompt"))
	if !errors.Is(err, ErrAuthentication) {
		t.Fatalf("expected authentication error, got %v", err)
	}
	if ErrorKind(err) != "authentication" {
		t.Fatalf("unexpected kind %q", ErrorKind(err))
	}
}

func TestRunCancellationKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "fake-claude")
	pidFile := filepath.Join(dir, "child.pid")
	script := "#!/bin/sh\nsleep 60 &\necho $! > '" + pidFile + "'\nwait\n"
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	started := time.Now()
	done := make(chan error, 1)
	go func() {
		_, _, _, _, err := Run(ctx, config.Agent{Adapter: "claude", Command: command}, dir, []byte("prompt"))
		done <- err
	}()
	for i := 0; i < 200; i++ {
		if _, err := os.Stat(pidFile); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if time.Since(started) > 4*time.Second {
		t.Fatal("cancellation took too long")
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		err = syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("child process %d survived cancellation: %v", pid, err)
}

func TestRunKillsLingeringProcessGroupAfterParentExit(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "fake-claude")
	pidFile := filepath.Join(dir, "child.pid")
	script := "#!/bin/sh\nsleep 60 &\necho $! > '" + pidFile + "'\nprintf '%s' '" + `{"structured_output":` + validReview + `}` + "'\n"
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := time.Now()
	result, _, _, _, err := Run(ctx, config.Agent{Adapter: "claude", Command: command}, dir, []byte("prompt"))
	if err != nil || result == nil || time.Since(started) > 4*time.Second {
		t.Fatalf("result=%#v err=%v elapsed=%s", result, err, time.Since(started))
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	for i := 0; i < 20; i++ {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("child process %d survived parent exit", pid)
}
