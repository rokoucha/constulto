package reviewer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

func runCommand(ctx context.Context, cmd *exec.Cmd) ([]byte, []byte, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	limitExceeded := make(chan struct{}, 1)
	stdout := &limitedBuffer{limit: outputLimit, exceeded: limitExceeded}
	stderr := &limitedBuffer{limit: outputLimit, exceeded: limitExceeded}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("%w: start agent: %v", ErrAgentFailed, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, exec.ErrWaitDelay) {
			err = nil
		}
		if stdout.Overflow() || stderr.Overflow() {
			return stdout.Bytes(), stderr.Bytes(), fmt.Errorf("%w: agent output exceeded %d bytes", ErrOutputLimit, outputLimit)
		}
		return stdout.Bytes(), stderr.Bytes(), err
	case <-limitExceeded:
		terminateProcessGroup(cmd, done)
		return stdout.Bytes(), stderr.Bytes(), fmt.Errorf("%w: agent output exceeded %d bytes", ErrOutputLimit, outputLimit)
	case <-ctx.Done():
		terminateProcessGroup(cmd, done)
		return stdout.Bytes(), stderr.Bytes(), ctx.Err()
	}
}

func terminateProcessGroup(cmd *exec.Cmd, done <-chan error) {
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-done:
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	case <-time.After(2 * time.Second):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
}

func classifyFailure(_ []byte, stderr []byte, runErr error) error {
	return classifyMessage(string(stderr), runErr)
}

func classifyMessage(raw string, fallback error) error {
	message := strings.TrimSpace(raw)
	if len(message) > 4096 {
		message = message[:4096] + "…"
	}
	lower := strings.ToLower(message)
	detail := message
	if detail == "" {
		detail = fallback.Error()
	}
	switch {
	case strings.Contains(lower, "auth"), strings.Contains(lower, "login"), strings.Contains(lower, "oauth"):
		return fmt.Errorf("%w: %s", ErrAuthentication, detail)
	case strings.Contains(lower, "permission"), strings.Contains(lower, "denied"), strings.Contains(lower, "not allowed"):
		return fmt.Errorf("%w: %s", ErrPermission, detail)
	case strings.Contains(lower, "rate limit"), strings.Contains(lower, "usage limit"):
		return fmt.Errorf("%w: %s", ErrRateLimit, detail)
	default:
		return fmt.Errorf("%w: %s", ErrAgentFailed, detail)
	}
}

func childEnv(current []string) []string {
	return replaceEnv(current, map[string]string{"CONSTULTO_DEPTH": "1"})
}

func replaceEnv(current []string, replacements map[string]string) []string {
	keys := make([]string, 0, len(replacements))
	for key := range replacements {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(current)+len(keys))
	for _, item := range current {
		key, _, _ := strings.Cut(item, "=")
		if _, replaced := replacements[key]; !replaced {
			result = append(result, item)
		}
	}
	for _, key := range keys {
		result = append(result, key+"="+replacements[key])
	}
	return result
}

func Restrictions(adapter string) []string {
	switch adapter {
	case "claude":
		return []string{"safe-mode", "restricted", "tools:Read,Glob,Grep", "permission:dontAsk", "mcp:none", "session:none"}
	case "muse":
		return []string{"write:disabled", "shell:disabled", "web:disabled", "foreign-context:none", "session-log:none", "approval:never"}
	case "opencode":
		return []string{"edit:deny", "bash:deny", "task:deny", "webfetch:deny", "websearch:deny", "external-directory:deny", "project-config:disabled", "global-config:isolated", "plugins:pure", "external-skills:disabled"}
	case "codex":
		return []string{"sandbox:read-only", "approval:never", "session:ephemeral", "user-config:ignored", "rules:ignored", "daemon:disabled"}
	case "antigravity":
		return []string{"mode:plan", "terminal:sandboxed", "slash-commands:disabled", "permissions:user-configured"}
	default:
		return nil
	}
}

type limitedBuffer struct {
	mu       sync.Mutex
	b        bytes.Buffer
	limit    int
	overflow bool
	exceeded chan<- struct{}
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	remain := b.limit - b.b.Len()
	if remain > 0 {
		if len(p) > remain {
			b.b.Write(p[:remain])
		} else {
			b.b.Write(p)
		}
	}
	if n > remain {
		b.overflow = true
		if b.exceeded != nil {
			select {
			case b.exceeded <- struct{}{}:
			default:
			}
		}
	}
	return n, nil
}

func (b *limitedBuffer) Overflow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.overflow
}
func (b *limitedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.b.Bytes()...)
}
