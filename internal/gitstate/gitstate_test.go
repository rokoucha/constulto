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

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

func TestWorkspaceReproducesTargetAndRecordsChanges(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init")
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("secret.env\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgSign=false", "commit", "-m", "initial")
	files := map[string]string{"tracked.txt": "after\n", "sub/included.txt": "included\n", "unselected.txt": "unselected\n", "secret.env": "secret\n"}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	statusBefore := runGit(t, dir, "status", "--porcelain")

	w, err := NewWorkspace(dir, []string{"sub/included.txt"})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"tracked.txt": "after\n", "sub/included.txt": "included\n"} {
		got, err := os.ReadFile(filepath.Join(w.Dir, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s: got %q, %v", name, got, err)
		}
	}
	for _, name := range []string{"unselected.txt", "secret.env"} {
		if _, err := os.Stat(filepath.Join(w.Dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s should not be copied: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(w.Dir, "tracked.txt"), []byte("experiment\n"), 0600); err != nil {
		t.Fatal(err)
	}
	changes, err := w.Changes()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(changes), "-after") || !strings.Contains(string(changes), "+experiment") || strings.Contains(string(changes), "included") {
		t.Fatalf("unexpected changes: %s", changes)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(w.Dir); !os.IsNotExist(err) {
		t.Fatalf("workspace not removed: %v", err)
	}
	if got := runGit(t, dir, "status", "--porcelain"); got != statusBefore {
		t.Fatalf("original repository changed: %q -> %q", statusBefore, got)
	}
}

func TestWorkspaceIncludedEdgeCases(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init")
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("original\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.local\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgSign=false", "commit", "-m", "initial")
	if err := os.WriteFile(filepath.Join(dir, "run.local"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}

	w, err := NewWorkspace(dir, []string{"link.txt", "run.local"})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	after, err := os.Stat(target)
	if err != nil || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("original file was rewritten through a symlink: %v", err)
	}
	if info, err := os.Lstat(filepath.Join(w.Dir, "link.txt")); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("included symlink should be copied as a regular file: %v %v", info, err)
	}
	info, err := os.Stat(filepath.Join(w.Dir, "run.local"))
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("included file mode not preserved: %v %v", info, err)
	}
	if err := os.WriteFile(filepath.Join(w.Dir, "run.local"), []byte("#!/bin/sh\necho changed\n"), 0755); err != nil {
		t.Fatal(err)
	}
	changes, err := w.Changes()
	if err != nil || !strings.Contains(string(changes), "+echo changed") {
		t.Fatalf("change to ignored included file not recorded: %s %v", changes, err)
	}
}

func TestWorkspaceDoesNotEscapeThroughParentSymlinkOrRunHooks(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	hooks := t.TempDir()
	marker := filepath.Join(t.TempDir(), "hook-ran")
	if err := os.WriteFile(filepath.Join(hooks, "post-checkout"), []byte("#!/bin/sh\ntouch "+marker+"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, []byte("[core]\n\thooksPath = "+hooks+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "init")
	if err := os.Symlink(outside, filepath.Join(dir, "linked")); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgSign=false", "commit", "-m", "initial")
	if err := os.MkdirAll(filepath.Join(dir, "real", "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "real", "nested", "file.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)

	w, err := NewWorkspace(dir, []string{"real/nested/file.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("post-checkout hook ran during workspace preparation: %v", err)
	}
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "linked", "new"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "linked", "new", "file.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := w.copyIncluded(source, "linked/new/file.txt"); err == nil || !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("expected copy through a parent symlink to be rejected, got %v", err)
	}
	w.Close()
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("created entries outside the workspace: %v", entries)
	}
}
