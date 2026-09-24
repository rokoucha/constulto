package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRejectsUnknownKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"surprise":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path, dir)
	if err == nil || !strings.Contains(err.Error(), "schema validation") {
		t.Fatalf("expected schema error, got %v", err)
	}
}

func TestProjectCannotSetCommands(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "constulto.json"), []byte(`{"version":1,"agents":{"bad":{"adapter":"claude","command":"evil"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Load("", dir)
	if err == nil || !strings.Contains(err.Error(), "project config") {
		t.Fatalf("expected project schema error, got %v", err)
	}
}

func TestMergeUserAndProject(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "user.json")
	if err := os.WriteFile(user, []byte(`{"version":1,"defaults":{"timeoutSeconds":12},"agents":{"reviewer":{"adapter":"claude","command":"fake","model":"m"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "constulto.json"), []byte(`{"version":1,"defaults":{"agent":"reviewer"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(user, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Config.Defaults.Agent != "reviewer" || got.Config.Defaults.TimeoutSeconds != 12 || got.Config.Agents["reviewer"].Command != "fake" {
		t.Fatalf("unexpected merge: %#v", got.Config)
	}
}

func TestExplicitZeroDisablesFollowups(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "user.json")
	if err := os.WriteFile(user, []byte(`{"version":1,"defaults":{"maxFollowups":0}}`), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(user, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Config.Defaults.MaxFollowups != 0 {
		t.Fatalf("maxFollowups=%d", got.Config.Defaults.MaxFollowups)
	}
	if err := os.WriteFile(filepath.Join(dir, "constulto.json"), []byte(`{"version":1,"defaults":{"maxFollowups":0}}`), 0600); err != nil {
		t.Fatal(err)
	}
	got, err = Load(user, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Config.Defaults.MaxFollowups != 0 {
		t.Fatalf("project maxFollowups=%d", got.Config.Defaults.MaxFollowups)
	}
}

func TestOpenCodeRequiresModel(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "user.json")
	if err := os.WriteFile(user, []byte(`{"version":1,"defaults":{"agent":"oc"},"agents":{"oc":{"adapter":"opencode","command":"opencode"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(user, dir); err == nil || !strings.Contains(err.Error(), "model") {
		t.Fatalf("expected missing model error, got %v", err)
	}
}

// TestCodexAdapterLoads はCodexアダプタ設定が正常に読み込めることを検証します。
func TestCodexAdapterLoads(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "user.json")
	if err := os.WriteFile(user, []byte(`{"version":1,"defaults":{"agent":"cx"},"agents":{"cx":{"adapter":"codex","command":"codex"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(user, dir)
	if err != nil {
		t.Fatalf("failed to load codex config: %v", err)
	}
	if got.Config.Agents["cx"].Adapter != "codex" || got.Config.Agents["cx"].Command != "codex" {
		t.Fatalf("unexpected codex agent config: %#v", got.Config.Agents["cx"])
	}
}

func TestCursorAdapterLoads(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "user.json")
	if err := os.WriteFile(user, []byte(`{"version":1,"defaults":{"agent":"cursor"},"agents":{"cursor":{"adapter":"cursor","command":"cursor-agent"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(user, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Config.Agents["cursor"] != (Agent{Adapter: "cursor", Command: "cursor-agent"}) {
		t.Fatalf("unexpected cursor agent config: %#v", got.Config.Agents["cursor"])
	}
}
