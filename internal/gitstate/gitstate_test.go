package gitstate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectImplementationIncludesWorkingTreeAndExplicitUntracked(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init")
	tracked := filepath.Join(dir, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "tracked.txt")
	runGit(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgSign=false", "commit", "-m", "initial")
	if err := os.WriteFile(tracked, []byte("after\n"), 0600); err != nil {
		t.Fatal(err)
	}
	untracked := filepath.Join(dir, "new file.txt")
	if err := os.WriteFile(untracked, []byte("new\n"), 0600); err != nil {
		t.Fatal(err)
	}

	target, err := Collect(dir, "implementation", "HEAD", []string{"new file.txt"}, []string{"ignored/**"}, []byte("brief"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(target.Diff), "+after") {
		t.Fatalf("missing working-tree diff: %s", target.Diff)
	}
	if !strings.Contains(string(target.IncludedText), "new file.txt") {
		t.Fatalf("missing include: %s", target.IncludedText)
	}
	if len(target.Untracked) != 0 {
		t.Fatalf("explicit include still listed as excluded: %v", target.Untracked)
	}
	if len(target.Excluded) != 1 || target.Excluded[0] != "ignored/**" {
		t.Fatalf("excluded patterns not recorded: %v", target.Excluded)
	}
}

func TestCollectRejectsIncludedSymlinkOutsideRepository(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	_, err := Collect(dir, "design", "", []string{"link.txt"}, nil, []byte("brief"))
	if err == nil || !strings.Contains(err.Error(), "resolves outside") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}

func TestCollectFingerprintIsIncludeOrderIndependentAndRejectsDuplicates(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{"a.txt": "a", "b.txt": "b"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	first, err := Collect(dir, "design", "", []string{"a.txt", "b.txt"}, nil, []byte("brief"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Collect(dir, "design", "", []string{"b.txt", "a.txt"}, nil, []byte("brief"))
	if err != nil || first.Fingerprint != second.Fingerprint {
		t.Fatalf("fingerprints differ: %s %s (%v)", first.Fingerprint, second.Fingerprint, err)
	}
	if _, err := Collect(dir, "design", "", []string{"a.txt", "a.txt"}, nil, []byte("brief")); err == nil {
		t.Fatal("expected duplicate include rejection")
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}
