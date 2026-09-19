package gitstate

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type Target struct {
	Root         string   `json:"root"`
	Base         string   `json:"base,omitempty"`
	MergeBase    string   `json:"mergeBase,omitempty"`
	Head         string   `json:"head,omitempty"`
	Status       string   `json:"status,omitempty"`
	Untracked    []string `json:"untracked,omitempty"`
	Included     []string `json:"included,omitempty"`
	Excluded     []string `json:"excluded,omitempty"`
	Fingerprint  string   `json:"fingerprint"`
	Diff         []byte   `json:"-"`
	IncludedText []byte   `json:"-"`
}

func Root(dir string) (string, error) {
	out, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("not a Git repository: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func Collect(root, kind, base string, includes, excludes []string, brief []byte) (Target, error) {
	includes = append([]string(nil), includes...)
	sort.Strings(includes)
	excludes = append([]string(nil), excludes...)
	sort.Strings(excludes)
	for i := 1; i < len(includes); i++ {
		if includes[i] == includes[i-1] {
			return Target{}, fmt.Errorf("included file %q was specified more than once", includes[i])
		}
	}
	t := Target{Root: root, Base: base, Excluded: append([]string(nil), excludes...)}
	hash := sha256.New()
	hashField(hash, "kind", []byte(kind))
	hashField(hash, "brief", brief)
	for _, pattern := range excludes {
		hashField(hash, "exclude", []byte(pattern))
	}
	if out, err := git(root, "ls-files", "--others", "--exclude-standard"); err == nil {
		t.Untracked = lines(out)
	}

	if kind == "implementation" {
		if base == "" {
			return Target{}, fmt.Errorf("--base is required for implementation review")
		}
		head, err := git(root, "rev-parse", "HEAD")
		if err != nil {
			return Target{}, fmt.Errorf("resolve HEAD: %w", err)
		}
		mb, err := git(root, "merge-base", base, "HEAD")
		if err != nil {
			return Target{}, fmt.Errorf("resolve merge-base with %q: %w", base, err)
		}
		t.Head, t.MergeBase = strings.TrimSpace(string(head)), strings.TrimSpace(string(mb))
		args := []string{"diff", "--binary", t.MergeBase, "--", "."}
		for _, pattern := range excludes {
			args = append(args, ":(exclude)"+pattern)
		}
		t.Diff, err = git(root, args...)
		if err != nil {
			return Target{}, fmt.Errorf("collect diff: %w", err)
		}
		status, err := git(root, "status", "--short", "--untracked-files=all")
		if err != nil {
			return Target{}, fmt.Errorf("collect status: %w", err)
		}
		t.Status = string(status)
		hashField(hash, "head", []byte(t.Head))
		hashField(hash, "merge-base", []byte(t.MergeBase))
		hashField(hash, "diff", t.Diff)
		hashField(hash, "status", []byte(t.Status))
	}

	for _, name := range includes {
		clean := filepath.Clean(name)
		absolute := clean
		if !filepath.IsAbs(clean) {
			absolute = filepath.Join(root, clean)
		}
		rel, err := filepath.Rel(root, absolute)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return Target{}, fmt.Errorf("included file %q is outside repository", name)
		}
		resolvedRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			return Target{}, fmt.Errorf("resolve repository root: %w", err)
		}
		resolved, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			return Target{}, fmt.Errorf("include %q: %w", name, err)
		}
		resolvedRel, err := filepath.Rel(resolvedRoot, resolved)
		if err != nil || resolvedRel == ".." || strings.HasPrefix(resolvedRel, ".."+string(filepath.Separator)) {
			return Target{}, fmt.Errorf("included file %q resolves outside repository", name)
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return Target{}, fmt.Errorf("include %q: %w", name, err)
		}
		if !info.Mode().IsRegular() {
			return Target{}, fmt.Errorf("include %q: not a regular file", name)
		}
		content, err := os.ReadFile(resolved)
		if err != nil {
			return Target{}, fmt.Errorf("include %q: %w", name, err)
		}
		t.Included = append(t.Included, filepath.ToSlash(rel))
		fmt.Fprintf(&includeBuffer{&t.IncludedText}, "\n--- included file: %s ---\n", filepath.ToSlash(rel))
		t.IncludedText = append(t.IncludedText, content...)
		hashField(hash, "include-name", []byte(filepath.ToSlash(rel)))
		hashField(hash, "include-content", content)
	}
	if len(t.Included) > 0 && len(t.Untracked) > 0 {
		included := make(map[string]bool, len(t.Included))
		for _, name := range t.Included {
			included[name] = true
		}
		filtered := t.Untracked[:0]
		for _, name := range t.Untracked {
			if !included[name] {
				filtered = append(filtered, name)
			}
		}
		t.Untracked = filtered
	}
	t.Fingerprint = hex.EncodeToString(hash.Sum(nil))
	return t, nil
}

type hashWriter interface{ Write([]byte) (int, error) }

func hashField(hash hashWriter, label string, value []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(label)))
	_, _ = hash.Write(size[:])
	_, _ = hash.Write([]byte(label))
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = hash.Write(size[:])
	_, _ = hash.Write(value)
}

type includeBuffer struct{ p *[]byte }

func (b *includeBuffer) Write(p []byte) (int, error) { *b.p = append(*b.p, p...); return len(p), nil }

func git(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func lines(out []byte) []string {
	var result []string
	for _, name := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		if name != "" {
			result = append(result, name)
		}
	}
	sort.Strings(result)
	return result
}
