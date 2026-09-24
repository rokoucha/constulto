package reviewer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rokoucha/constulto/internal/config"
)

func TestAntigravity(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "agy")
	script := `#!/bin/sh
case "$1" in
  --version) echo 'agy 1.2.6'; exit 0 ;;
  --help) echo '--input-format --output-format --json-schema --model --mode --sandbox --disable-slash-commands'; exit 0 ;;
esac
for flag in --mode=plan --sandbox --disable-slash-commands --input-format --output-format --json-schema --model; do
  case " $* " in *" $flag "*) ;; *) echo "missing $flag" >&2; exit 2;; esac
done
case "$*" in *--dangerously-skip-permissions*) exit 2;; esac
read input
case "$input" in *'"event":"user"'*'sample prompt'*) ;; *) exit 2;; esac
echo '{"event":"init"}'
echo '{"event":"result","result":{"status":"SUCCESS","structured_output":{"summary":"ok","findings":[],"decisions":[],"scope":{"reviewed":[],"notReviewed":[],"testsNotRun":[]}}}}'
`
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	probe := ProbeAgent(context.Background(), config.Agent{Adapter: "antigravity", Command: command})
	if !probe.Supported || probe.Version != "agy 1.2.6" {
		t.Fatalf("probe: %+v", probe)
	}
	review, metadata, _, stderr, err := Run(context.Background(), config.Agent{Adapter: "antigravity", Command: command, Model: "test-model"}, dir, []byte("sample prompt"))
	if err != nil {
		t.Fatalf("run: %v; stderr: %s", err, stderr)
	}
	if review.Summary != "ok" || metadata.Model != "test-model" {
		t.Fatalf("review=%+v metadata=%+v", review, metadata)
	}
}

func TestParseAntigravityFailure(t *testing.T) {
	_, _, err := parseAntigravity([]byte("{\"event\":\"result\",\"result\":{\"status\":\"ERROR\",\"error\":\"authentication required\"}}\n"), "")
	if !errors.Is(err, ErrAuthentication) {
		t.Fatalf("expected authentication failure, got %v", err)
	}
	_, _, err = parseAntigravity([]byte("{\"event\":\"result\",\"result\":{\"status\":\"SUCCESS\"}}\n"), "")
	if !errors.Is(err, ErrInvalidOutput) || !strings.Contains(err.Error(), "structured") {
		t.Fatalf("expected missing structured output, got %v", err)
	}
}
