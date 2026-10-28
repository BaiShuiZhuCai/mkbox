package system

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/makebox/makebox/internal/logx"
)

// FS performs filesystem changes with backups and dry-run support.
type FS struct {
	DryRun bool
}

// Write atomically writes data to path, backing up any existing file first.
func (f *FS) Write(path string, data []byte, mode os.FileMode) error {
	old, readErr := os.ReadFile(path)
	if readErr == nil && bytes.Equal(old, data) {
		return nil
	}

	st, existed := fileExistsStat(path)
	if existed {
		mode = st.Mode().Perm()
	}

	if f.DryRun {
		logx.Infof("[dry-run] 写入 %s (mode %04o)%s", path, mode, backupNote(existed))
		printPreview(data)
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建目录 %s: %w", filepath.Dir(path), err)
	}

	if existed {
		bak := BackupPath(path)
		if err := copyFile(path, bak, st.Mode().Perm()); err != nil {
			return fmt.Errorf("备份 %s: %w", path, err)
		}
		logx.Debugf(true, "已备份 %s -> %s", path, bak)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".mkbox-*")
	if err != nil {
		return fmt.Errorf("创建临时文件: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入 %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("写入 %s: %w", path, err)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return fmt.Errorf("设置权限 %s: %w", path, err)
	}
	if existed && st.Sys() != nil {
		if sys, ok := st.Sys().(*syscall.Stat_t); ok {
			_ = os.Chown(tmpName, int(sys.Uid), int(sys.Gid))
		}
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("替换 %s: %w", path, err)
	}
	return nil
}

func backupNote(existed bool) string {
	if existed {
		return "（原文件已备份）"
	}
	return ""
}

func printPreview(data []byte) {
	const max = 4000
	s := string(data)
	truncated := false
	if len(s) > max {
		s = s[:max]
		truncated = true
	}
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		logx.Raw("    | %s", line)
	}
	if truncated {
		logx.Raw("    | ... (truncated)")
	}
}

// Remove backs up and deletes path.
func (f *FS) Remove(path string) error {
	_, existed := fileExistsStat(path)
	if !existed {
		return nil
	}
	if f.DryRun {
		logx.Infof("[dry-run] 删除 %s（原文件已备份）", path)
		return nil
	}
	bak := BackupPath(path)
	if err := copyFile(path, bak, fileMode(path, 0o644)); err != nil {
		return fmt.Errorf("备份 %s: %w", path, err)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("删除 %s: %w", path, err)
	}
	return nil
}

// EnsureDir creates a directory if needed.
func (f *FS) EnsureDir(path string, mode os.FileMode) error {
	if f.DryRun {
		logx.Infof("[dry-run] 创建目录 %s (mode %04o)", path, mode)
		return nil
	}
	return os.MkdirAll(path, mode)
}
