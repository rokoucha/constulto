package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rokoucha/constulto/internal/gitstate"
	"github.com/rokoucha/constulto/internal/runstore"
)

func TestReviewDryRunDoesNotInvokeAgent(t *testing.T) {
	dir := t.TempDir()
	brief := filepath.Join(dir, "brief.md")
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(brief, []byte("Review this design."), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"version":1,"agents":{"x":{"adapter":"claude","command":"must-not-run"}},"defaults":{"agent":"x"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	app := App{Stdout: &out, Stderr: &stderr, Getwd: func() (string, error) { return dir, nil }}
	code := app.Run(context.Background(), []string{"review", "--kind", "design", "--brief", brief, "--config", configPath, "--dry-run"})
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(out.String(), "accessible scope: repository read/search") || !strings.Contains(out.String(), "restrictions: safe-mode") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestReviewRejectsRecursiveInvocation(t *testing.T) {
	t.Setenv("CONSTULTO_DEPTH", "1")
	var stderr bytes.Buffer
	app := App{Stdout: &bytes.Buffer{}, Stderr: &stderr, Getwd: os.Getwd}
	if code := app.Run(context.Background(), []string{"review"}); code != 2 || !strings.Contains(stderr.String(), "recursive") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestReviewManyReturns130WhenProbeIsCancelled(t *testing.T) {
	dir := t.TempDir()
	brief := filepath.Join(dir, "brief.md")
	configPath := filepath.Join(dir, "config.json")
	_ = os.WriteFile(brief, []byte("Review."), 0600)
	cfg := `{"version":1,"agents":{"a":{"adapter":"claude","command":"missing-a"},"b":{"adapter":"claude","command":"missing-b"}}}`
	_ = os.WriteFile(configPath, []byte(cfg), 0600)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stderr bytes.Buffer
	app := App{Stdout: &bytes.Buffer{}, Stderr: &stderr, Getwd: func() (string, error) { return dir, nil }}
	code := app.Run(ctx, []string{"review", "--kind", "design", "--brief", brief, "--config", configPath, "--agent", "a", "--agent", "b"})
	if code != 130 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestReviewDryRunJSONIsOneDocument(t *testing.T) {
	dir := t.TempDir()
	brief := filepath.Join(dir, "brief.md")
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(brief, []byte("Review this design."), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"version":1,"agents":{"x":{"adapter":"claude","command":"must-not-run"}},"defaults":{"agent":"x"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	app := App{Stdout: &out, Stderr: &stderr, Getwd: func() (string, error) { return dir, nil }}
	code := app.Run(context.Background(), []string{"review", "--kind", "design", "--brief", brief, "--config", configPath, "--dry-run", "--json"})
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var got struct {
		Plans []reviewPlan `json:"plans"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || len(got.Plans) != 1 || got.Plans[0].Agent != "x" {
		t.Fatalf("invalid plan JSON: %v %s", err, out.String())
	}
}

func TestBuildPromptRequestsReviewBeyondListedPerspectives(t *testing.T) {
	prompt, err := buildPrompt("design", gitstate.Target{}, []byte("Check this design."))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prompt), "観点をレビュー範囲の上限と解釈せず") || !strings.Contains(string(prompt), "欠けた前提・観点・反例") {
		t.Fatalf("critical review instruction is missing: %s", prompt)
	}
}

func TestReviewHelpSucceeds(t *testing.T) {
	var out, stderr bytes.Buffer
	app := App{Stdout: &out, Stderr: &stderr, Getwd: os.Getwd}
	if code := app.Run(context.Background(), []string{"review", "--help"}); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(out.String(), "レビュー種別") || !strings.Contains(out.String(), "--include") {
		t.Fatalf("unexpected help: %s", out.String())
	}
}

func TestKongTopLevelAliasesAndValidation(t *testing.T) {
	for _, tt := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"help", "review"}, 0, "レビュー種別"},
		{[]string{"--version"}, 0, Version},
		{[]string{"review", "--kind", "invalid", "--brief", "x"}, 2, "must be one of"},
		{nil, 2, "Usage: constulto"},
	} {
		var out, stderr bytes.Buffer
		app := App{Stdout: &out, Stderr: &stderr, Getwd: os.Getwd}
		if code := app.Run(context.Background(), tt.args); code != tt.code || !strings.Contains(out.String()+stderr.String(), tt.want) {
			t.Fatalf("args=%v code=%d out=%s stderr=%s", tt.args, code, out.String(), stderr.String())
		}
	}
}

func TestSkillDryRunShowsDifferenceAndForceUpdates(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".codex", "skills", "constulto", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("modified\n"), 0644); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	app := App{Stdout: &out, Stderr: &stderr, Getwd: os.Getwd}
	if code := app.Run(context.Background(), []string{"skill", "install", "--agent", "codex", "--dry-run"}); code != 0 || !strings.Contains(out.String(), "--- installed") {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), stderr.String())
	}
	out.Reset()
	stderr.Reset()
	if code := app.Run(context.Background(), []string{"skill", "install", "--agent", "codex", "--force"}); code != 0 {
		t.Fatalf("code=%d err=%s", code, stderr.String())
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "constulto レビュー") {
		t.Fatalf("not updated: %s", content)
	}
}

func TestReviewEndToEndWithFakeCLI(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	t.Setenv("XDG_STATE_HOME", state)
	brief := filepath.Join(dir, "brief.md")
	command := filepath.Join(dir, "fake-claude")
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(brief, []byte("Review this design."), 0600); err != nil {
		t.Fatal(err)
	}
	review := `{"summary":"checked","findings":[],"decisions":[],"scope":{"reviewed":["brief"],"notReviewed":["runtime"],"testsNotRun":["integration"]}}`
	script := "#!/bin/sh\ncase \"$1\" in\n --version) echo '1.0' ;;\n --help) echo '--print --safe-mode --restricted --strict-mcp-config --mcp-config --tools --permission-mode --permission-prompts --disable-slash-commands --no-session-persistence --output-format --json-schema --model' ;;\n *) printf '%s' '" + `{"structured_output":` + review + `}` + "' ;;\nesac\n"
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{"version": 1, "defaults": map[string]any{"agent": "x", "maxFollowups": 2}, "agents": map[string]any{"x": map[string]any{"adapter": "claude", "command": command}}}
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(configPath, b, 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	app := App{Stdout: &out, Stderr: &stderr, Getwd: func() (string, error) { return dir, nil }}
	code := app.Run(context.Background(), []string{"review", "--kind", "design", "--brief", brief, "--config", configPath})
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(out.String(), "status: completed") || !strings.Contains(out.String(), "none reported (not proof of correctness)") {
		t.Fatalf("unexpected output: %s", out.String())
	}
	entries, err := os.ReadDir(filepath.Join(state, "constulto", "runs"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("runs: %v %v", entries, err)
	}
	store := runstore.Store{Root: filepath.Join(state, "constulto", "runs")}
	_, result, _, err := store.Load(entries[0].Name())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" || result.Review == nil || len(result.Restrictions) == 0 {
		t.Fatalf("bad saved result: %#v", result)
	}
	rebuttal := filepath.Join(dir, "rebuttal.md")
	if err := os.WriteFile(rebuttal, []byte("New evidence."), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	stderr.Reset()
	code = app.Run(context.Background(), []string{"followup", entries[0].Name(), "--brief", rebuttal, "--config", configPath})
	if code != 0 {
		t.Fatalf("followup code=%d stderr=%s", code, stderr.String())
	}
	entries, err = os.ReadDir(filepath.Join(state, "constulto", "runs"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("followup runs: %v %v", entries, err)
	}
	var followupID string
	for _, entry := range entries {
		if entry.Name() != result.RunID {
			followupID = entry.Name()
		}
	}
	_, followup, _, err := store.Load(followupID)
	if err != nil {
		t.Fatal(err)
	}
	if followup.Status != "completed" {
		t.Fatalf("bad followup: %#v", followup)
	}
	out.Reset()
	stderr.Reset()
	code = app.Run(context.Background(), []string{"followup", followupID, "--brief", rebuttal, "--config", configPath})
	if code != 0 {
		t.Fatalf("second-level followup code=%d stderr=%s", code, stderr.String())
	}
	out.Reset()
	stderr.Reset()
	code = app.Run(context.Background(), []string{"followup", result.RunID, "--brief", rebuttal, "--config", configPath})
	if code != 2 || !strings.Contains(stderr.String(), "maximum followups exceeded") {
		t.Fatalf("expected total followup limit, code=%d stderr=%s", code, stderr.String())
	}
}

func TestReviewManyKeepsPartialResultsAndSupportsSelectedFollowup(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	t.Setenv("XDG_STATE_HOME", state)
	brief := filepath.Join(dir, "brief.md")
	rebuttal := filepath.Join(dir, "rebuttal.md")
	goodCommand := filepath.Join(dir, "good-claude")
	badCommand := filepath.Join(dir, "bad-claude")
	configPath := filepath.Join(dir, "config.json")
	for path, content := range map[string]string{brief: "Review this design.", rebuttal: "New evidence."} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	review := `{"summary":"checked","findings":[],"decisions":[],"scope":{"reviewed":["brief"],"notReviewed":[],"testsNotRun":[]}}`
	help := "--print --safe-mode --restricted --strict-mcp-config --mcp-config --tools --permission-mode --permission-prompts --disable-slash-commands --no-session-persistence --output-format --json-schema --model"
	goodScript := "#!/bin/sh\ncase \"$1\" in\n --version) echo 1.0 ;;\n --help) echo '" + help + "' ;;\n *) printf '%s' '" + `{"structured_output":` + review + `}` + "' ;;\nesac\n"
	badScript := "#!/bin/sh\ncase \"$1\" in\n --version) echo 1.0 ;;\n --help) echo '" + help + "' ;;\n *) echo failed >&2; exit 7 ;;\nesac\n"
	if err := os.WriteFile(goodCommand, []byte(goodScript), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(badCommand, []byte(badScript), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{
		"version":  1,
		"defaults": map[string]any{"agent": "good", "maxConcurrency": 2},
		"agents": map[string]any{
			"good": map[string]any{"adapter": "claude", "command": goodCommand},
			"bad":  map[string]any{"adapter": "claude", "command": badCommand},
		},
	}
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(configPath, b, 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	app := App{Stdout: &out, Stderr: &stderr, Getwd: func() (string, error) { return dir, nil }}
	code := app.Run(context.Background(), []string{"review", "--kind", "design", "--brief", brief, "--config", configPath, "--agent", "good", "--agent", "bad"})
	if code != 1 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(out.String(), "status: partial") || !strings.Contains(out.String(), "== good (claude): completed ==") || !strings.Contains(out.String(), "== bad (claude): failed ==") {
		t.Fatalf("unexpected output: %s", out.String())
	}
	entries, err := os.ReadDir(filepath.Join(state, "constulto", "runs"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("runs: %v %v", entries, err)
	}
	store := runstore.Store{Root: filepath.Join(state, "constulto", "runs")}
	manifest, result, runDir, err := store.Load(entries[0].Name())
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Status != "partial" || len(result.AgentResults) != 2 || result.AgentResults[0].Review == nil {
		t.Fatalf("bad aggregate: %#v %#v", manifest, result)
	}
	agentDirs, err := os.ReadDir(filepath.Join(runDir, "agents"))
	if err != nil || len(agentDirs) != 2 {
		t.Fatalf("agent records: %v %v", agentDirs, err)
	}

	out.Reset()
	stderr.Reset()
	code = app.Run(context.Background(), []string{"followup", manifest.ID, "--brief", rebuttal, "--config", configPath})
	if code != 2 || !strings.Contains(stderr.String(), "--agent is required") {
		t.Fatalf("missing selector code=%d stderr=%s", code, stderr.String())
	}
	out.Reset()
	stderr.Reset()
	code = app.Run(context.Background(), []string{"followup", manifest.ID, "--brief", rebuttal, "--config", configPath, "--agent", "good"})
	if code != 0 {
		t.Fatalf("followup code=%d stderr=%s", code, stderr.String())
	}
}

func TestReviewMarksResultStaleWhenIncludedFileChanges(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	t.Setenv("XDG_STATE_HOME", state)
	brief := filepath.Join(dir, "brief.md")
	included := filepath.Join(dir, "included.txt")
	command := filepath.Join(dir, "fake-claude")
	configPath := filepath.Join(dir, "config.json")
	_ = os.WriteFile(brief, []byte("Review."), 0600)
	_ = os.WriteFile(included, []byte("before"), 0600)
	review := `{"summary":"checked","findings":[],"decisions":[],"scope":{"reviewed":[],"notReviewed":[],"testsNotRun":[]}}`
	script := "#!/bin/sh\ncase \"$1\" in\n --version) echo 1.0 ;;\n --help) echo '--print --safe-mode --restricted --strict-mcp-config --mcp-config --tools --permission-mode --permission-prompts --disable-slash-commands --no-session-persistence --output-format --json-schema --model' ;;\n *) printf changed > '" + included + "'; printf '%s' '" + `{"structured_output":` + review + `}` + "' ;;\nesac\n"
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{"version": 1, "defaults": map[string]any{"agent": "x"}, "agents": map[string]any{"x": map[string]any{"adapter": "claude", "command": command}}}
	b, _ := json.Marshal(cfg)
	_ = os.WriteFile(configPath, b, 0600)
	var out, stderr bytes.Buffer
	app := App{Stdout: &out, Stderr: &stderr, Getwd: func() (string, error) { return dir, nil }}
	code := app.Run(context.Background(), []string{"review", "--kind", "design", "--brief", brief, "--include", "included.txt", "--config", configPath})
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(out.String(), "stale: target changed") {
		t.Fatalf("not stale: %s", out.String())
	}
}
