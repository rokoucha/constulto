package reviewer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rokoucha/constulto/internal/config"
)

func TestCursor(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "cursor-agent")
	script := `#!/bin/sh
case "$1" in
  --version) echo '2026.09.23-86fc751'; exit 0 ;;
  --help) echo '--print --output-format --mode --sandbox --workspace --model --trust'; exit 0 ;;
esac
case " $* " in
  *' --print --output-format json --mode ask --sandbox enabled --trust --workspace '*' --model test-model '*) ;;
  *) echo "unexpected arguments: $*" >&2; exit 2 ;;
esac
case " $* " in *' --force '*|*' --yolo '*) exit 2 ;; esac
test -f "$CURSOR_CONFIG_DIR/cli-config.json" || exit 2
test -f "$CURSOR_CONFIG_DIR/workspace/.cursor/hooks.json" || exit 2
grep -q '"Shell(\*)"' "$CURSOR_CONFIG_DIR/cli-config.json" || exit 2
grep -q 'subagentStart' "$CURSOR_CONFIG_DIR/workspace/.cursor/hooks.json" || exit 2
test -L "$CURSOR_CONFIG_DIR/workspace/cursor-agent" || exit 2
input=$(cat)
case "$input" in *'sample prompt'*'review-result.json'*'"required"'*) ;; *) echo 'missing prompt or schema' >&2; exit 2;; esac
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"{\"summary\":\"ok\",\"findings\":[],\"decisions\":[],\"scope\":{\"reviewed\":[],\"notReviewed\":[],\"testsNotRun\":[]}}"}'
`
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	probe := ProbeAgent(context.Background(), config.Agent{Adapter: "cursor", Command: command})
	if !probe.Supported || probe.Version != "2026.09.23-86fc751" {
		t.Fatalf("probe: %+v", probe)
	}
	review, metadata, _, stderr, err := Run(context.Background(), config.Agent{Adapter: "cursor", Command: command, Model: "test-model"}, dir, []byte("sample prompt"))
	if err != nil {
		t.Fatalf("run: %v; stderr: %s", err, stderr)
	}
	if review.Summary != "ok" || metadata.Model != "test-model" {
		t.Fatalf("review=%+v metadata=%+v", review, metadata)
	}
}

func TestParseCursorRejectsInvalidResult(t *testing.T) {
	for _, raw := range []string{
		`{"type":"result","subtype":"success","result":"{\"summary\":\"incomplete\"}"}`,
		`{"type":"assistant","result":"{}"}`,
		`not json`,
	} {
		_, err := parseCursor([]byte(raw))
		if !errors.Is(err, ErrInvalidOutput) {
			t.Fatalf("%s: expected invalid output, got %v", raw, err)
		}
	}
	_, err := parseCursor([]byte(`{"type":"result","subtype":"error","is_error":true,"result":"authentication required"}`))
	if !errors.Is(err, ErrAuthentication) {
		t.Fatalf("expected authentication failure, got %v", err)
	}
}

func TestParseCursorAcceptsPreamble(t *testing.T) {
	result := "Reading the file first.\n" + validReview
	raw, err := json.Marshal(map[string]any{"type": "result", "subtype": "success", "result": result})
	if err != nil {
		t.Fatal(err)
	}
	review, err := parseCursor(raw)
	if err != nil || review.Summary != "checked" {
		t.Fatalf("review=%+v err=%v", review, err)
	}
}

func TestProbeCursorRejectsMissingFlag(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "cursor-agent")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then echo 1; else echo '--print --output-format --mode --workspace --model --trust'; fi\n"), 0700); err != nil {
		t.Fatal(err)
	}
	probe := probeCursor(context.Background(), command)
	if probe.Supported || !strings.Contains(probe.Error, "--sandbox") {
		t.Fatalf("probe: %+v", probe)
	}
}
