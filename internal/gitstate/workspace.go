package gitstate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Workspace はレビュー対象の作業ツリーを複製した使い捨てのGitリポジトリです。
type Workspace struct {
	Dir      string
	baseline string
}

// workspaceGit はユーザーのGit設定によるフックや署名を無効化してクローンを操作します。
// クローンの準備はCodexのsandboxの外で実行されるため、post-checkoutなどのフックも走らせない。
func workspaceGit(dir string, input []byte, args ...string) ([]byte, error) {
	config := []string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "commit.gpgSign=false",
		"-c", "user.name=constulto",
		"-c", "user.email=constulto@localhost",
	}
	return gitInput(dir, input, append(config, args...)...)
}

// NewWorkspace はHEADをcloneし、追跡中ファイルの未commitの変更と明示されたファイルを再現します。
// ignoredファイルと明示されていない未追跡ファイルは複製しません。
func NewWorkspace(root string, included []string) (*Workspace, error) {
	head, err := git(root, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("resolve HEAD: %w", err)
	}
	patch, err := git(root, "diff", "--no-ext-diff", "--binary", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("collect working tree changes: %w", err)
	}
	dir, err := os.MkdirTemp("", "constulto-workspace-")
	if err != nil {
		return nil, err
	}
	w := &Workspace{Dir: dir}
	if err := w.populate(root, strings.TrimSpace(string(head)), patch, included); err != nil {
		w.Close()
		return nil, err
	}
	return w, nil
}

func (w *Workspace) populate(root, head string, patch []byte, included []string) error {
	if _, err := workspaceGit(root, nil, "clone", "--quiet", "--shared", "--no-checkout", root, w.Dir); err != nil {
		return err
	}
	if _, err := workspaceGit(w.Dir, nil, "checkout", "--quiet", "--detach", head); err != nil {
		return err
	}
	if len(patch) > 0 {
		if _, err := workspaceGit(w.Dir, patch, "apply", "--binary", "--whitespace=nowarn"); err != nil {
			return fmt.Errorf("apply working tree changes: %w", err)
		}
	}
	for _, name := range included {
		if err := w.copyIncluded(root, name); err != nil {
			return fmt.Errorf("copy included file %q: %w", name, err)
		}
	}
	if _, err := workspaceGit(w.Dir, nil, "add", "--all"); err != nil {
		return err
	}
	// ignoredな明示ファイルも追跡対象にして、レビュアーの変更をChangesで拾えるようにする。
	if len(included) > 0 {
		if _, err := workspaceGit(w.Dir, nil, append([]string{"add", "--force", "--"}, included...)...); err != nil {
			return err
		}
	}
	if _, err := workspaceGit(w.Dir, nil, "commit", "--quiet", "--no-verify", "--allow-empty", "-m", "constulto baseline"); err != nil {
		return err
	}
	baseline, err := git(w.Dir, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	w.baseline = strings.TrimSpace(string(baseline))
	return nil
}

// copyIncluded は明示ファイルを権限ごと複製します。
// 複製先のパスにcheckout済みのシンボリックリンクがあっても、それを辿って複製の外へ書き込まない。
func (w *Workspace) copyIncluded(root, name string) error {
	src := filepath.Join(root, filepath.FromSlash(name))
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	content, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	dst := filepath.Join(w.Dir, filepath.FromSlash(name))
	// MkdirAllは既存のシンボリックリンクを辿るので、作成前に既存の祖先がクローン内に解決される事を確かめる。
	existing := filepath.Dir(dst)
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return err
		}
		existing = filepath.Dir(existing)
	}
	resolvedRoot, err := filepath.EvalSymlinks(w.Dir)
	if err != nil {
		return err
	}
	resolvedDir, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return err
	}
	if rel, err := filepath.Rel(resolvedRoot, resolvedDir); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("destination directory resolves outside the workspace")
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// maxChangesPatch を超える差分は、記録先を圧迫しないよう変更ファイルの一覧に置き換えます。
const maxChangesPatch = 4 << 20

// Changes はレビュアーが複製に加えた変更を、複製直後の状態からの差分として返します。
// 新規のignoredファイルは含みません。
func (w *Workspace) Changes() ([]byte, error) {
	if _, err := workspaceGit(w.Dir, nil, "add", "--all"); err != nil {
		return nil, err
	}
	patch, err := workspaceGit(w.Dir, nil, "diff", "--no-ext-diff", "--cached", "--binary", w.baseline)
	if err != nil || len(patch) <= maxChangesPatch {
		return patch, err
	}
	stat, err := workspaceGit(w.Dir, nil, "diff", "--no-ext-diff", "--cached", "--stat=200", w.baseline)
	if err != nil {
		return nil, err
	}
	return append([]byte(fmt.Sprintf("# patch omitted: %d bytes exceeds %d bytes; changed files follow\n", len(patch), maxChangesPatch)), stat...), nil
}

func (w *Workspace) Close() error {
	return os.RemoveAll(w.Dir)
}
