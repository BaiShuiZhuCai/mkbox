// Package backup creates and restores tar.gz snapshots of the configuration
// files managed by mkbox. It deliberately ignores runtime data such as images,
// container layers and k3s/etcd data directories.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/makebox/makebox/internal/config"
	"github.com/makebox/makebox/internal/k8s"
	"github.com/makebox/makebox/internal/logx"
	"github.com/makebox/makebox/internal/proxy"
	"github.com/makebox/makebox/internal/system"
)

// DefaultDir is where mkbox stores its snapshots.
const DefaultDir = "/var/backups/makebox"

// maxRestoreFileSize bounds a single extracted file to avoid a malicious or
// corrupt archive filling the disk.
const maxRestoreFileSize = 512 << 20

// Options configures Create.
type Options struct {
	Paths   []string
	Dir     string
	Output  string
	DryRun  bool
	Verbose bool
}

// Result describes a created snapshot.
type Result struct {
	Archive string
	Files   int
	Bytes   int64
	Skipped int
}

// Item is a snapshot found in the backup directory.
type Item struct {
	Path    string
	Size    int64
	ModTime time.Time
}

// RestoreOptions configures Restore.
type RestoreOptions struct {
	Archive string
	Dest    string
	DryRun  bool
	Verbose bool
}

// RestoreResult describes a restore operation.
type RestoreResult struct {
	Archive string
	Files   int
	Bytes   int64
	Skipped int
}

// DefaultPaths lists the files and directories included in a snapshot. It
// covers mkbox changes without copying container or cluster data.
func DefaultPaths() []string {
	home := system.UserHome()
	paths := []string{
		"/etc/docker/daemon.json",
		"/etc/containerd/config.toml",
		"/etc/containerd/certs.d",
		"/etc/systemd/system/docker.service.d",
		"/etc/systemd/system/containerd.service.d",
		"/etc/kubernetes",
		"/etc/makebox",
		"/etc/rancher/k3s",
		k8s.SysctlFile,
		k8s.ModulesFile,
		"/etc/apt/sources.list",
		"/etc/apt/sources.list.d",
		proxy.AptConf,
		"/etc/pacman.d/mirrorlist",
		"/etc/apk/repositories",
		"/etc/pip.conf",
		proxy.EnvironmentFile,
		proxy.EnvFile,
		"/etc/profile.d/mkbox-go.sh",
		"/var/lib/kubelet/config.yaml",
		"/var/lib/kubelet/kubeadm-flags.env",
		config.Path(),
	}
	if home != "" {
		paths = append(paths, filepath.Join(home, ".kube", "config"))
		if home != "/root" {
			paths = append(paths, "/root/.kube/config")
		}
	}
	return paths
}

func uniquePaths(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		clean := filepath.Clean(p)
		if !filepath.IsAbs(clean) {
			logx.Warnf("忽略非绝对路径: %s", p)
			continue
		}
		if seen[clean] {
			continue
		}
		seen[clean] = true
		out = append(out, clean)
	}
	return out
}

// Create writes a gzip-compressed tar archive of opts.Paths (or DefaultPaths).
func Create(opts Options) (*Result, error) {
	explicit := len(opts.Paths) > 0
	paths := uniquePaths(opts.Paths)
	if explicit && len(paths) == 0 {
		return nil, fmt.Errorf("--path 必须使用绝对路径")
	}
	if len(paths) == 0 {
		paths = uniquePaths(DefaultPaths())
	}
	dir := opts.Dir
	if dir == "" {
		dir = DefaultDir
	}
	archive := opts.Output
	if archive == "" {
		if !filepath.IsAbs(dir) {
			return nil, fmt.Errorf("备份目录必须是绝对路径: %s", dir)
		}
		name := "mkbox-backup-" + time.Now().Format("20060102-150405") + ".tar.gz"
		archive = uniqueArchive(filepath.Join(dir, name))
	}
	if !filepath.IsAbs(archive) {
		return nil, fmt.Errorf("备份文件必须是绝对路径: %s", archive)
	}

	if opts.DryRun {
		res, err := scan(paths)
		if err != nil {
			return nil, err
		}
		if explicit && res.Files == 0 {
			return nil, fmt.Errorf("指定的备份路径中没有可备份的普通文件")
		}
		res.Archive = archive
		logx.Infof("[dry-run] 将创建备份 %s", archive)
		logx.Infof("[dry-run] 包含 %d 个文件，约 %s", res.Files, humanBytes(res.Bytes))
		if res.Skipped > 0 {
			logx.Infof("[dry-run] 将跳过 %d 个符号链接/特殊文件", res.Skipped)
		}
		for _, p := range paths {
			logx.Debugf(opts.Verbose, "备份路径: %s", p)
		}
		return res, nil
	}

	if err := os.MkdirAll(filepath.Dir(archive), 0o700); err != nil {
		return nil, fmt.Errorf("创建备份目录: %w", err)
	}
	tmp := archive + ".tmp"
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("创建备份文件 %s: %w", tmp, err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()

	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	res := &Result{Archive: archive}
	seen := make(map[string]bool)
	for _, root := range paths {
		if _, err := os.Lstat(root); err != nil {
			if !os.IsNotExist(err) {
				logx.Warnf("跳过 %s: %v", root, err)
			}
			continue
		}
		if err := addPath(tw, root, seen, res, opts.Verbose); err != nil {
			tw.Close()
			gz.Close()
			f.Close()
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		gz.Close()
		f.Close()
		return nil, fmt.Errorf("写入归档: %w", err)
	}
	if err := gz.Close(); err != nil {
		f.Close()
		return nil, fmt.Errorf("压缩归档: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("关闭备份文件: %w", err)
	}
	if explicit && res.Files == 0 {
		return nil, fmt.Errorf("指定的备份路径中没有可备份的普通文件")
	}
	if err := os.Rename(tmp, archive); err != nil {
		return nil, fmt.Errorf("保存备份 %s: %w", archive, err)
	}
	ok = true

	logx.Successf("备份完成: %s（%d 个文件，%s）", archive, res.Files, humanBytes(res.Bytes))
	if res.Skipped > 0 {
		logx.Debugf(opts.Verbose, "跳过 %d 个符号链接/特殊文件", res.Skipped)
	}
	return res, nil
}

func uniqueArchive(p string) string {
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return p
	}
	base := strings.TrimSuffix(p, ".tar.gz")
	for i := 1; i < 1000; i++ {
		candidate := fmt.Sprintf("%s-%d.tar.gz", base, i)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
	return fmt.Sprintf("%s-%d.tar.gz", base, time.Now().UnixNano())
}

func addPath(tw *tar.Writer, root string, seen map[string]bool, res *Result, verbose bool) error {
	return filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			logx.Warnf("跳过 %s: %v", p, err)
			if d != nil && d.IsDir() && p != root {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			logx.Warnf("读取 %s: %v", p, err)
			return nil
		}
		if info.IsDir() {
			name := archiveName(p)
			if name == "" || seen[name] {
				return nil
			}
			seen[name] = true
			hdr, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return nil
			}
			hdr.Name = name + "/"
			hdr.Uid, hdr.Gid = 0, 0
			hdr.Uname, hdr.Gname = "", ""
			if err := tw.WriteHeader(hdr); err != nil {
				return fmt.Errorf("写入目录 %s: %w", p, err)
			}
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			res.Skipped++
			logx.Debugf(verbose, "跳过符号链接 %s", p)
			return nil
		}
		if !info.Mode().IsRegular() {
			res.Skipped++
			logx.Debugf(verbose, "跳过特殊文件 %s", p)
			return nil
		}
		name := archiveName(p)
		if name == "" || seen[name] {
			return nil
		}
		seen[name] = true
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return nil
		}
		hdr.Name = name
		hdr.Uid, hdr.Gid = 0, 0
		hdr.Uname, hdr.Gname = "", ""
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("写入 %s: %w", p, err)
		}
		f, err := os.Open(p)
		if err != nil {
			return fmt.Errorf("读取 %s: %w", p, err)
		}
		n, copyErr := io.Copy(tw, f)
		closeErr := f.Close()
		if copyErr != nil {
			return fmt.Errorf("归档 %s: %w", p, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("关闭 %s: %w", p, closeErr)
		}
		res.Files++
		res.Bytes += n
		logx.Debugf(verbose, "已加入 %s (%s)", p, humanBytes(n))
		return nil
	})
}

func archiveName(p string) string {
	clean := filepath.ToSlash(filepath.Clean(p))
	clean = strings.TrimPrefix(clean, "/")
	if clean == "." || clean == "" || strings.HasPrefix(clean, "../") {
		return ""
	}
	return clean
}

func scan(paths []string) (*Result, error) {
	res := &Result{}
	for _, root := range paths {
		if _, err := os.Lstat(root); err != nil {
			continue
		}
		err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			if info.Mode().IsRegular() {
				res.Files++
				res.Bytes += info.Size()
			} else if !info.IsDir() {
				res.Skipped++
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return res, nil
}

// List returns snapshots in dir, newest first.
func List(dir string) ([]Item, error) {
	if dir == "" {
		dir = DefaultDir
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取备份目录 %s: %w", dir, err)
	}
	items := make([]Item, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tar.gz") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, Item{
			Path:    filepath.Join(dir, e.Name()),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].ModTime.After(items[j].ModTime)
	})
	return items, nil
}

// Restore extracts a snapshot. Existing files are backed up individually by
// system.FS before being replaced; symlinks and special files are skipped.
func Restore(opts RestoreOptions) (*RestoreResult, error) {
	if opts.Archive == "" {
		return nil, errors.New("必须指定备份文件")
	}
	dest := opts.Dest
	if dest == "" {
		dest = "/"
	}
	if !filepath.IsAbs(dest) {
		return nil, fmt.Errorf("恢复目标必须是绝对路径: %s", dest)
	}
	dest = filepath.Clean(dest)

	f, err := os.Open(opts.Archive)
	if err != nil {
		return nil, fmt.Errorf("打开备份 %s: %w", opts.Archive, err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("备份文件不是有效的 gzip 归档: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	fs := &system.FS{DryRun: opts.DryRun}
	res := &RestoreResult{Archive: opts.Archive}

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("读取归档: %w", err)
		}
		name, err := safeRel(hdr.Name)
		if err != nil {
			return nil, fmt.Errorf("拒绝不安全的归档路径 %q: %w", hdr.Name, err)
		}
		target := filepath.Join(dest, filepath.FromSlash(name))
		if !withinRoot(dest, target) {
			return nil, fmt.Errorf("归档路径越界: %q", hdr.Name)
		}
		mode := hdr.FileInfo().Mode().Perm()
		switch hdr.Typeflag {
		case tar.TypeDir:
			if mode == 0 {
				mode = 0o755
			}
			if opts.DryRun {
				logx.Infof("[dry-run] 创建目录 %s", target)
				continue
			}
			if _, err := os.Stat(target); err == nil {
				continue
			}
			if err := os.MkdirAll(target, mode); err != nil {
				return nil, fmt.Errorf("创建目录 %s: %w", target, err)
			}
			_ = os.Chmod(target, mode)
		case tar.TypeReg:
			if hdr.Size > maxRestoreFileSize {
				return nil, fmt.Errorf("归档文件过大: %s (%s)", target, humanBytes(hdr.Size))
			}
			data, err := io.ReadAll(io.LimitReader(tr, maxRestoreFileSize+1))
			if err != nil {
				return nil, fmt.Errorf("读取 %s: %w", target, err)
			}
			if int64(len(data)) > maxRestoreFileSize {
				return nil, fmt.Errorf("归档文件过大: %s", target)
			}
			if mode == 0 {
				mode = 0o644
			}
			if opts.DryRun {
				logx.Infof("[dry-run] 恢复 %s (%s)", target, humanBytes(int64(len(data))))
				res.Files++
				res.Bytes += int64(len(data))
				continue
			}
			if err := fs.Write(target, data, mode); err != nil {
				return nil, err
			}
			res.Files++
			res.Bytes += int64(len(data))
			logx.Debugf(opts.Verbose, "已恢复 %s", target)
		default:
			res.Skipped++
			logx.Debugf(opts.Verbose, "跳过 %s (类型 %c)", hdr.Name, hdr.Typeflag)
		}
	}

	if opts.DryRun {
		logx.Infof("[dry-run] 将从 %s 恢复 %d 个文件到 %s", opts.Archive, res.Files, dest)
	} else {
		logx.Successf("已恢复 %d 个文件（%s）到 %s", res.Files, humanBytes(res.Bytes), dest)
	}
	if res.Skipped > 0 {
		logx.Warnf("跳过 %d 个符号链接/特殊文件", res.Skipped)
	}
	return res, nil
}

func safeRel(name string) (string, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	name = strings.TrimPrefix(name, "./")
	if name == "" || name == "." {
		return "", errors.New("空路径")
	}
	if strings.HasPrefix(name, "/") {
		return "", errors.New("不允许绝对路径")
	}
	clean := path.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("不允许路径穿越")
	}
	if clean == "." {
		return "", errors.New("空路径")
	}
	return clean, nil
}

func withinRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 5; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
