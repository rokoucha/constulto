package runstore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rokoucha/constulto/internal/gitstate"
)

func TestNewUsesPrivatePermissions(t *testing.T) {
	s := Store{Root: filepath.Join(t.TempDir(), "runs")}
	m, dir, err := s.New(Manifest{Kind: "design", Target: gitstate.Target{Fingerprint: "abc"}}, []byte("brief"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.ID == "" {
		t.Fatal("missing ID")
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("directory mode = %o", info.Mode().Perm())
	}
	info, err = os.Stat(filepath.Join(dir, "brief.md"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("file mode = %o", info.Mode().Perm())
	}
}

func TestLoadRejectsTraversalRunID(t *testing.T) {
	s := Store{Root: filepath.Join(t.TempDir(), "runs")}
	if _, _, _, err := s.Load(".."); err == nil {
		t.Fatal("expected invalid run ID")
	}
}

func TestNewKeepsDiffAndIncludedFilesSeparate(t *testing.T) {
	s := Store{Root: filepath.Join(t.TempDir(), "runs")}
	_, dir, err := s.New(Manifest{Kind: "implementation", Target: gitstate.Target{Fingerprint: "abc"}}, []byte("brief"), []byte("diff"), []byte("included"))
	if err != nil {
		t.Fatal(err)
	}
	diff, err := os.ReadFile(filepath.Join(dir, "diff.patch"))
	if err != nil {
		t.Fatal(err)
	}
	included, err := os.ReadFile(filepath.Join(dir, "included-files.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(diff) != "diff" || string(included) != "included" {
		t.Fatalf("unexpected artifacts: %q %q", diff, included)
	}
}

func TestFinishPreservesRawOutputWhenResultValidationFails(t *testing.T) {
	s := Store{Root: filepath.Join(t.TempDir(), "runs")}
	m, dir, err := s.New(Manifest{Kind: "design", Target: gitstate.Target{Fingerprint: "abc"}}, []byte("brief"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(&m, RunResult{}, []byte("raw answer"), []byte("diagnostics")); err == nil {
		t.Fatal("expected validation failure")
	}
	for name, want := range map[string]string{"answer.txt": "raw answer", "diagnostics.log": "diagnostics"} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s=%q err=%v", name, got, err)
		}
	}
}
