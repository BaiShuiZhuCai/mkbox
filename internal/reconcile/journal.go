package reconcile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/makebox/makebox/internal/execx"
	"github.com/makebox/makebox/internal/logx"
)

// Journal status values.
const (
	JournalRunning        = "running"
	JournalApplied        = "applied"
	JournalFailed         = "failed"
	JournalRolledBack     = "rolled-back"
	JournalRollbackFailed = "rollback-failed"
)

// DefaultStateDir is where mkbox keeps journals and reports.
const DefaultStateDir = "/var/lib/mkbox"

// MaxSnapshotSize bounds how much of a managed file is copied into a journal.
const MaxSnapshotSize = 2 << 20

// FileSnapshot is a restorable copy of a managed file.
type FileSnapshot struct {
	Path       string `json:"path"`
	Existed    bool   `json:"existed"`
	Mode       uint32 `json:"mode,omitempty"`
	Data       []byte `json:"data,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
	CapturedAt string `json:"capturedAt"`
}

// JournalAction records one action's lifecycle.
type JournalAction struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Risk       string `json:"risk,omitempty"`
	Reversible bool   `json:"reversible"`
	Status     string `json:"status"`
	StartedAt  string `json:"startedAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Journal is an apply transaction that can be inspected and rolled back.
type Journal struct {
	Version      int             `json:"version"`
	ID           string          `json:"id"`
	CreatedAt    string          `json:"createdAt"`
	UpdatedAt    string          `json:"updatedAt"`
	Scope        Scope           `json:"scope"`
	Status       string          `json:"status"`
	Hostname     string          `json:"hostname,omitempty"`
	Plan         PlanSummary     `json:"plan"`
	Actions      []JournalAction `json:"actions"`
	Files        []FileSnapshot  `json:"files,omitempty"`
	Services     map[string]bool `json:"servicesBefore,omitempty"`
	Error        string          `json:"error,omitempty"`
	RolledBackAt string          `json:"rolledBackAt,omitempty"`
}

// PlanSummary is the compact form of the plan stored in a journal.
type PlanSummary struct {
	GeneratedAt string   `json:"generatedAt"`
	Actions     []Action `json:"actions"`
	Warnings    []string `json:"warnings,omitempty"`
}

// JournalSummary is a lightweight journal listing entry.
type JournalSummary struct {
	ID        string `json:"id"`
	CreatedAt string `json:"createdAt"`
	Scope     Scope  `json:"scope"`
	Status    string `json:"status"`
	Actions   int    `json:"actions"`
	Error     string `json:"error,omitempty"`
}

// JournalStore persists journals under a state directory.
type JournalStore struct {
	Dir             string
	AllowedPrefixes []string
}

// NewJournalStore returns a store rooted at stateDir. An empty stateDir uses
// MKBOX_STATE_DIR or /var/lib/mkbox.
func NewJournalStore(stateDir string) *JournalStore {
	if strings.TrimSpace(stateDir) == "" {
		stateDir = os.Getenv("MKBOX_STATE_DIR")
	}
	if strings.TrimSpace(stateDir) == "" {
		stateDir = DefaultStateDir
	}
	return &JournalStore{
		Dir:             filepath.Join(stateDir, "journals"),
		AllowedPrefixes: []string{"/etc"},
	}
}

var journalIDPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,79}$`)

// Begin creates and persists a new journal for a plan. services records which
// services were active before apply so rollback only restarts those.
func (s *JournalStore) Begin(plan Plan, services map[string]bool, now time.Time) (*Journal, error) {
	if now.IsZero() {
		now = time.Now()
	}
	hostname, _ := os.Hostname()
	j := &Journal{
		Version:   1,
		ID:        newJournalID(now),
		CreatedAt: now.UTC().Format(time.RFC3339),
		UpdatedAt: now.UTC().Format(time.RFC3339),
		Scope:     plan.Scope,
		Status:    JournalRunning,
		Hostname:  hostname,
		Plan: PlanSummary{
			GeneratedAt: plan.GeneratedAt,
			Actions:     append([]Action(nil), plan.Actions...),
			Warnings:    append([]string(nil), plan.Warnings...),
		},
		Services: services,
	}
	for _, a := range plan.Actions {
		j.Actions = append(j.Actions, JournalAction{
			ID:         a.ID,
			Title:      a.Title,
			Risk:       a.Risk,
			Reversible: a.Reversible,
			Status:     "pending",
		})
	}
	if err := s.Save(j); err != nil {
		return nil, err
	}
	return j, nil
}

func newJournalID(now time.Time) string {
	buf := make([]byte, 4)
	if f, err := os.Open("/dev/urandom"); err == nil {
		_, _ = f.Read(buf)
		f.Close()
	}
	return now.UTC().Format("20060102T150405Z") + "-" + fmt.Sprintf("%x", buf)
}

func (s *JournalStore) path(id string) (string, error) {
	if !journalIDPattern.MatchString(id) || strings.Contains(id, "..") {
		return "", fmt.Errorf("无效的 journal ID %q", id)
	}
	return filepath.Join(s.Dir, id+".json"), nil
}

// Save writes a journal atomically with restrictive permissions.
func (s *JournalStore) Save(j *Journal) error {
	if j == nil {
		return fmt.Errorf("journal 为空")
	}
	path, err := s.path(j.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return fmt.Errorf("创建 journal 目录: %w", err)
	}
	_ = os.Chmod(s.Dir, 0o700)
	j.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return fmt.Errorf("编码 journal: %w", err)
	}
	tmp, err := os.CreateTemp(s.Dir, ".journal-*")
	if err != nil {
		return fmt.Errorf("创建 journal 临时文件: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入 journal: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("写入 journal: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("设置 journal 权限: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("保存 journal: %w", err)
	}
	return nil
}

// Load reads a journal by ID.
func (s *JournalStore) Load(id string) (*Journal, error) {
	path, err := s.path(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("未找到 journal %s（目录 %s）", id, s.Dir)
		}
		return nil, fmt.Errorf("读取 journal %s: %w", id, err)
	}
	var j Journal
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, fmt.Errorf("解析 journal %s: %w（文件可能损坏）", id, err)
	}
	if j.ID != id {
		return nil, fmt.Errorf("journal %s 内容 ID 不匹配（%s），已拒绝加载", id, j.ID)
	}
	return &j, nil
}

// Latest returns the most recent journal.
func (s *JournalStore) Latest() (*Journal, error) {
	list, err := s.List()
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("没有可用的 journal（目录 %s）", s.Dir)
	}
	return s.Load(list[0].ID)
}

// List returns journals newest first.
func (s *JournalStore) List() ([]JournalSummary, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取 journal 目录: %w", err)
	}
	out := make([]JournalSummary, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		j, err := s.Load(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		out = append(out, JournalSummary{
			ID:        j.ID,
			CreatedAt: j.CreatedAt,
			Scope:     j.Scope,
			Status:    j.Status,
			Actions:   len(j.Actions),
			Error:     j.Error,
		})
	}
	sort.Slice(out, func(i, k int) bool { return out[i].CreatedAt > out[k].CreatedAt })
	return out, nil
}

// ManagedFilesForAction lists files snapshotted before an action executes.
func ManagedFilesForAction(id string) []string {
	switch id {
	case "docker.install", "docker.configure":
		return []string{dockerDaemonPath, dockerProxyPath}
	case "docker.start":
		return nil
	case "docker.proxy.set", "docker.proxy.clear":
		return []string{dockerProxyPath}
	case "containerd.configure":
		return []string{containerdConfigPath, containerdHostsPath}
	case "containerd.proxy.set", "containerd.proxy.clear":
		return []string{containerdProxyPath}
	case "k8s.install":
		return []string{
			containerdConfigPath, containerdHostsPath,
			"/etc/apt/sources.list.d/kubernetes.list",
			"/etc/yum.repos.d/kubernetes.repo",
			"/etc/zypp/repos.d/kubernetes.repo",
			"/etc/sysctl.d/99-mkbox-k8s.conf",
			"/etc/modules-load.d/mkbox-k8s.conf",
		}
	case "k8s.preflight":
		return []string{
			"/etc/fstab",
			"/etc/sysctl.d/99-mkbox-k8s.conf",
			"/etc/modules-load.d/mkbox-k8s.conf",
			"/etc/selinux/config",
		}
	case "k8s.init":
		return []string{"/etc/kubernetes/admin.conf", "/etc/makebox/kubeadm-init.yaml", "/etc/makebox/join.sh"}
	case "k8s.start":
		return nil
	}
	return nil
}

const (
	dockerDaemonPath     = "/etc/docker/daemon.json"
	dockerProxyPath      = "/etc/systemd/system/docker.service.d/http-proxy.conf"
	containerdConfigPath = "/etc/containerd/config.toml"
	containerdHostsPath  = "/etc/containerd/certs.d/docker.io/hosts.toml"
	containerdProxyPath  = "/etc/systemd/system/containerd.service.d/http-proxy.conf"
)

// Snapshot captures paths into the journal exactly once. Files outside the
// configured prefixes are rejected so a plan or journal cannot write anywhere.
func (s *JournalStore) Snapshot(j *Journal, paths []string, now time.Time) error {
	if now.IsZero() {
		now = time.Now()
	}
	known := make(map[string]bool, len(j.Files))
	for _, f := range j.Files {
		known[f.Path] = true
	}
	for _, path := range paths {
		clean := filepath.Clean(path)
		if !s.allowed(clean) {
			return fmt.Errorf("拒绝快照受管范围之外的文件 %s", path)
		}
		if known[clean] {
			continue
		}
		snap := FileSnapshot{Path: clean, CapturedAt: now.UTC().Format(time.RFC3339)}
		st, err := os.Stat(clean)
		switch {
		case err == nil && st.IsDir():
			continue
		case err == nil:
			snap.Existed = true
			snap.Mode = uint32(st.Mode().Perm())
			if st.Size() > MaxSnapshotSize {
				snap.Truncated = true
			} else {
				data, err := os.ReadFile(clean)
				if err != nil {
					return fmt.Errorf("快照 %s: %w", clean, err)
				}
				snap.Data = data
			}
		case errors.Is(err, os.ErrNotExist):
			// Record absence so rollback can delete files created by apply.
		default:
			return fmt.Errorf("检查 %s: %w", clean, err)
		}
		j.Files = append(j.Files, snap)
		known[clean] = true
	}
	return nil
}

func (s *JournalStore) allowed(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	clean := filepath.Clean(path)
	for _, prefix := range s.AllowedPrefixes {
		prefix = filepath.Clean(prefix)
		if clean == prefix || strings.HasPrefix(clean, prefix+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// RollbackOptions controls journal restoration.
type RollbackOptions struct {
	Force   bool
	DryRun  bool
	Now     time.Time
	Restart func([]string) error
	Out     func(format string, args ...any)
}

// RollbackResult reports what rollback changed.
type RollbackResult struct {
	JournalID string   `json:"journalId"`
	Status    string   `json:"status"`
	Restored  []string `json:"restored,omitempty"`
	Removed   []string `json:"removed,omitempty"`
	Skipped   []string `json:"skipped,omitempty"`
	Services  []string `json:"servicesRestarted,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
	DryRun    bool     `json:"dryRun,omitempty"`
}

// Rollback restores snapshotted files and restarts services that were active
// before apply. Irreversible actions require Force and are reported honestly.
func (s *JournalStore) Rollback(id string, opts RollbackOptions) (*Journal, *RollbackResult, error) {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	if opts.Out == nil {
		opts.Out = func(string, ...any) {}
	}
	j, err := s.Load(id)
	if err != nil {
		return nil, nil, err
	}
	if j.Status == JournalRolledBack {
		return j, nil, fmt.Errorf("journal %s 已经回滚过", id)
	}
	res := &RollbackResult{JournalID: j.ID, DryRun: opts.DryRun}
	var irreversible []string
	for _, a := range j.Actions {
		if !a.Reversible && a.Status == "applied" {
			irreversible = append(irreversible, a.Title)
		}
	}
	if len(irreversible) > 0 {
		res.Warnings = append(res.Warnings,
			"以下动作无法自动撤销（软件包/集群/内核状态）: "+strings.Join(irreversible, "；"))
		if !opts.Force {
			return j, res, fmt.Errorf("journal 包含不可逆动作，请人工确认后使用 --force: %s", strings.Join(irreversible, "；"))
		}
	}

	// Restore files in reverse capture order: later snapshot wins if a path
	// were captured twice (Snapshot dedupes, so this is a safety net).
	for i := len(j.Files) - 1; i >= 0; i-- {
		snap := j.Files[i]
		if !s.allowed(snap.Path) {
			res.Skipped = append(res.Skipped, snap.Path+"（超出受管范围）")
			continue
		}
		if snap.Truncated {
			res.Skipped = append(res.Skipped, snap.Path+"（快照过大，无法恢复）")
			continue
		}
		if opts.DryRun {
			if snap.Existed {
				res.Restored = append(res.Restored, snap.Path)
			} else {
				res.Removed = append(res.Removed, snap.Path)
			}
			continue
		}
		if snap.Existed {
			if err := restoreFile(snap); err != nil {
				res.Status = JournalRollbackFailed
				j.Status = JournalRollbackFailed
				j.Error = err.Error()
				_ = s.Save(j)
				return j, res, fmt.Errorf("恢复 %s: %w", snap.Path, err)
			}
			res.Restored = append(res.Restored, snap.Path)
			opts.Out("已恢复 %s", snap.Path)
		} else {
			if err := os.Remove(snap.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
				res.Status = JournalRollbackFailed
				j.Status = JournalRollbackFailed
				j.Error = err.Error()
				_ = s.Save(j)
				return j, res, fmt.Errorf("删除 %s: %w", snap.Path, err)
			}
			res.Removed = append(res.Removed, snap.Path)
			opts.Out("已删除 %s", snap.Path)
		}
	}

	var active []string
	for name, wasActive := range j.Services {
		if wasActive {
			active = append(active, name)
		}
	}
	sort.Strings(active)
	if len(active) > 0 && opts.Restart != nil {
		if opts.DryRun {
			res.Services = active
		} else {
			if err := opts.Restart(active); err != nil {
				res.Status = JournalRollbackFailed
				j.Status = JournalRollbackFailed
				j.Error = err.Error()
				_ = s.Save(j)
				return j, res, fmt.Errorf("回滚后重启服务失败: %w", err)
			}
			res.Services = active
			opts.Out("已重启服务: %s", strings.Join(active, ", "))
		}
	}

	if !opts.DryRun {
		j.Status = JournalRolledBack
		j.RolledBackAt = opts.Now.UTC().Format(time.RFC3339)
		j.Error = ""
		if err := s.Save(j); err != nil {
			return j, res, err
		}
	}
	res.Status = j.Status
	if opts.DryRun {
		res.Status = "dry-run"
	}
	return j, res, nil
}

func restoreFile(snap FileSnapshot) error {
	dir := filepath.Dir(snap.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建目录 %s: %w", dir, err)
	}
	mode := os.FileMode(snap.Mode)
	if mode == 0 {
		mode = 0o644
	}
	var old os.FileInfo
	if st, err := os.Stat(snap.Path); err == nil {
		old = st
	}
	tmp, err := os.CreateTemp(dir, ".mkbox-restore-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(snap.Data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	if old != nil && os.Geteuid() == 0 {
		_ = os.Chown(tmpName, fileUID(old), fileGID(old))
	}
	if err := os.Rename(tmpName, snap.Path); err != nil {
		return err
	}
	return nil
}

// ServiceStates probes which services are active. It is used to decide what
// rollback should restart.
func ServiceStates(r *execx.Runner, services []string) map[string]bool {
	out := map[string]bool{}
	for _, name := range services {
		if name == "" {
			continue
		}
		active := false
		if r != nil {
			if stdout, err := r.Output("systemctl", "is-active", name); err == nil {
				active = strings.TrimSpace(stdout) == "active"
			}
		}
		out[name] = active
	}
	return out
}

// PlanServices returns the sorted union of services touched by a plan.
func PlanServices(plan Plan) []string {
	set := map[string]bool{}
	for _, a := range plan.Actions {
		for _, s := range ServiceForAction(a.ID) {
			set[s] = true
		}
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// RestartSystemd restarts services through systemctl unless the runner is in
// dry-run mode.
func RestartSystemd(r *execx.Runner, services []string) error {
	for _, name := range services {
		if err := r.Run("systemctl", "restart", name); err != nil {
			return fmt.Errorf("重启 %s: %w", name, err)
		}
		logx.Infof("已重启 %s", name)
	}
	return nil
}
