package reviewer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rokoucha/constulto/internal/config"
)

func TestParseOpenCodeCompletedReview(t *testing.T) {
	raw := []byte(`{"type":"step_start","part":{"type":"step-start"}}
{"type":"text","part":{"text":"{\"summary\":\"ok\",\"findings\":[],\"decisions\":[],\"scope\":{\"reviewed\":[],\"notReviewed\":[],\"testsNotRun\":[]}}"}}
{"type":"step_finish","part":{"reason":"stop","cost":0.25}}
`)
	review, metadata, err := parseOpenCode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if review.Summary != "ok" || metadata.ProviderCostUSD == nil || *metadata.ProviderCostUSD != 0.25 {
		t.Fatalf("unexpected result: %#v %#v", review, metadata)
	}
}

func TestParseOpenCodeRequiresSuccessfulFinish(t *testing.T) {
	raw := []byte(`{"type":"text","part":{"text":"{}"}}
{"type":"step_finish","part":{"reason":"error"}}
`)
	_, _, err := parseOpenCode(raw)
	if !errors.Is(err, ErrAgentFailed) {
		t.Fatalf("expected agent failure, got %v", err)
	}
}

func TestParseOpenCodeClassifiesErrorEvent(t *testing.T) {
	_, _, err := parseOpenCode([]byte(`{"type":"error","error":{"message":"usage limit reached"}}` + "\n"))
	if !errors.Is(err, ErrRateLimit) {
		t.Fatalf("expected rate-limit error, got %v", err)
	}
}

func TestProbeOpenCodeRequires118(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "fake-opencode")
	script := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 2.0.0; else echo '--pure --format --agent --dir --model'; fi\n"
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	probe := probeOpenCode(context.Background(), command)
	if probe.Supported || !bytes.Contains([]byte(probe.Error), []byte("1.18.x")) {
		t.Fatalf("unexpected probe: %#v", probe)
	}
}

func TestOpenCodeConfigDeniesActiveCapabilities(t *testing.T) {
	configRaw, raw, err := openCodeConfig("provider/model")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(configRaw, []byte(`"model":"provider/model"`)) {
		t.Fatalf("configured model missing: %s", configRaw)
	}
	for _, capability := range []string{`"edit":"deny"`, `"bash":"deny"`, `"task":"deny"`, `"webfetch":"deny"`, `"websearch":"deny"`, `"external_directory":"deny"`} {
		if !bytes.Contains(raw, []byte(capability)) {
			t.Fatalf("permission missing %s: %s", capability, raw)
		}
	}
}

func TestOpenCodeWorkspaceExcludesRepositoryConfiguration(t *testing.T) {
	root := t.TempDir()
	configDir := t.TempDir()
	for _, name := range []string{"source.go", "opencode.json", "AGENTS.md"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, ".opencode"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	workspace, err := openCodeWorkspace(configDir, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "source.go")); err != nil {
		t.Fatalf("source not accessible: %v", err)
	}
	for _, name := range []string{"opencode.json", "AGENTS.md", ".opencode", ".git"} {
		if _, err := os.Lstat(filepath.Join(workspace, name)); !os.IsNotExist(err) {
			t.Fatalf("%s was not excluded: %v", name, err)
		}
	}
}

func TestStripJSONFence(t *testing.T) {
	if got := stripJSONFence("```json\n{\"x\":1}\n```"); got != `{"x":1}` {
		t.Fatalf("got %q", got)
	}
}

func TestRunOpenCodeUsesRestrictedEnvironment(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "fake-opencode")
	review := `{\"summary\":\"checked\",\"findings\":[],\"decisions\":[],\"scope\":{\"reviewed\":[],\"notReviewed\":[],\"testsNotRun\":[]}}`
	script := `#!/bin/sh
case "$OPENCODE_PERMISSION" in
  *'"edit":"deny"'*'"task":"deny"'*'"webfetch":"deny"'*) ;;
  *) echo 'unsafe permissions' >&2; exit 3 ;;
esac
printf '%s\n' '{"type":"step_start","part":{}}'
printf '%s\n' '{"type":"text","part":{"text":"` + review + `"}}'
printf '%s\n' '{"type":"step_finish","part":{"reason":"stop","cost":0.1}}'
`
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, metadata, _, _, err := Run(ctx, config.Agent{Adapter: "opencode", Command: command, Model: "provider/model"}, dir, []byte("prompt"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary != "checked" || metadata.Model != "unknown" || len(metadata.ObservedModels) != 0 || metadata.ProviderCostUSD == nil {
		t.Fatalf("unexpected result: %#v %#v", result, metadata)
	}
}
