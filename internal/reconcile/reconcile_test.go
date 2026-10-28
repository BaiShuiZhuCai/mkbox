package reconcile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/makebox/makebox/internal/config"
)

func dockerProfile(scope Scope) Profile {
	return Profile{
		Scope: scope,
		Docker: DockerProfile{
			InstallMirror: "aliyun",
			Mirrors:       []string{"https://mirror.example"},
			LogDriver:     "json-file",
			LogMaxSize:    "10m",
			LogMaxFile:    "3",
		},
	}
}

func TestPlanDockerFromScratch(t *testing.T) {
	p, err := BuildPlan(dockerProfile(ScopeDocker), Observed{}, nil, PlanOptions{Now: time.Unix(0, 0)})
	if err != nil {
		t.Fatal(err)
	}
	ids := actionIDs(p)
	want := []string{"docker.install", "docker.configure"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("actions = %v, want %v", ids, want)
	}
	if len(p.Diffs) != 1 || p.Diffs[0].Path != "/etc/docker/daemon.json" {
		t.Fatalf("diffs = %+v", p.Diffs)
	}
	if !strings.Contains(p.Diffs[0].Unified, "registry-mirrors") {
		t.Fatalf("diff missing mirrors: %s", p.Diffs[0].Unified)
	}
	if p.GeneratedAt == "" || p.Host.Hostname == "" {
		t.Fatalf("plan metadata missing: %+v", p)
	}
}

func TestPlanDockerConverged(t *testing.T) {
	profile := dockerProfile(ScopeDocker)
	desired, err := DesiredDaemonJSON("", profile.Docker)
	if err != nil {
		t.Fatal(err)
	}
	obs := Observed{
		DockerInstalled:       true,
		DockerRunning:         true,
		DockerDaemonValid:     true,
		DockerProxyConfigured: false,
		daemonJSON:            desired,
	}
	p, err := BuildPlan(profile, obs, nil, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Empty() {
		t.Fatalf("expected converged plan, got %v", actionIDs(p))
	}
}

func TestPlanKubernetesFromScratch(t *testing.T) {
	profile := Profile{
		Scope: ScopeK8s,
		Docker: DockerProfile{
			Mirrors: []string{"https://mirror.example"},
		},
		K8s: K8sProfile{Distro: "kubeadm", Version: "1.35", CNI: "flannel"},
	}
	p, err := BuildPlan(profile, Observed{}, nil, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ids := strings.Join(actionIDs(p), ",")
	for _, want := range []string{"k8s.install", "k8s.preflight", "k8s.init"} {
		if !strings.Contains(ids, want) {
			t.Fatalf("actions %s missing %s", ids, want)
		}
	}
	if !p.HasIrreversible() {
		t.Fatal("install/init should be irreversible")
	}
}

func TestPlanExistingClusterWarnsAndStarts(t *testing.T) {
	profile := Profile{
		Scope: ScopeK8s,
		K8s:   K8sProfile{Distro: "kubeadm", Version: "1.35"},
	}
	obs := Observed{ClusterInitialized: true, ClusterKubeconfig: "/etc/kubernetes/admin.conf"}
	p, err := BuildPlan(profile, obs, nil, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(actionIDs(p), ",") != "k8s.start" {
		t.Fatalf("actions = %v", actionIDs(p))
	}
	if len(p.Warnings) == 0 || !strings.Contains(strings.Join(p.Warnings, "\n"), "不会重装") {
		t.Fatalf("expected existing-cluster warning, got %v", p.Warnings)
	}
}

func TestDesiredDaemonJSONPreservesUnknownKeys(t *testing.T) {
	current := `{"bip":"10.10.0.1/24","registry-mirrors":["https://old.example"]}`
	out, err := DesiredDaemonJSON(current, DockerProfile{
		Mirrors:    []string{"https://new.example"},
		LogDriver:  "json-file",
		LogMaxSize: "5m",
		LogMaxFile: "2",
	})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatal(err)
	}
	if m["bip"] != "10.10.0.1/24" {
		t.Fatalf("unknown key lost: %v", m)
	}
	if !strings.Contains(out, "https://new.example") || strings.Contains(out, "https://old.example") {
		t.Fatalf("mirrors not replaced: %s", out)
	}
}

func TestDiffUnified(t *testing.T) {
	d := DiffUnified("x", "a\nb\n", "a\nc\n")
	for _, want := range []string{"--- a/x", "+++ b/x", "-b", "+c"} {
		if !strings.Contains(d, want) {
			t.Fatalf("diff missing %q:\n%s", want, d)
		}
	}
	if got := DiffUnified("x", "a\n", "a\n"); got != "" {
		t.Fatalf("identical content should produce empty diff: %q", got)
	}
}

func actionIDs(p Plan) []string {
	out := make([]string, 0, len(p.Actions))
	for _, a := range p.Actions {
		out = append(out, a.ID)
	}
	return out
}

func testStore(t *testing.T) (*JournalStore, string) {
	t.Helper()
	root := t.TempDir()
	s := NewJournalStore(filepath.Join(root, "state"))
	s.AllowedPrefixes = []string{root}
	return s, root
}

func testPlan(actions ...Action) Plan {
	return Plan{
		GeneratedAt: "2026-01-01T00:00:00Z",
		Scope:       ScopeAll,
		Actions:     actions,
	}
}

func TestJournalRoundTripAndPermissions(t *testing.T) {
	s, root := testStore(t)
	p := testPlan(Action{ID: "docker.configure", Title: "configure", Reversible: true})
	j, err := s.Begin(p, map[string]bool{"docker": true}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "daemon.json")
	if err := os.WriteFile(path, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := s.Snapshot(j, []string{path}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(j); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Load(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Files) != 1 || string(loaded.Files[0].Data) != "old" {
		t.Fatalf("snapshot not persisted: %+v", loaded.Files)
	}
	info, err := os.Stat(filepath.Join(s.Dir, j.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("journal mode = %o, want 600", info.Mode().Perm())
	}
	if _, err := s.List(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Latest(); err != nil {
		t.Fatal(err)
	}
}

func TestJournalRejectsTraversalAndForeignPaths(t *testing.T) {
	s, _ := testStore(t)
	p := testPlan(Action{ID: "docker.configure", Title: "configure", Reversible: true})
	j, err := s.Begin(p, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load("../../etc/passwd"); err == nil {
		t.Fatal("path traversal must be rejected")
	}
	if err := s.Snapshot(j, []string{"/root/.ssh/id_ed25519"}, time.Now()); err == nil {
		t.Fatal("snapshot outside prefix must be rejected")
	}
}

func TestRollbackRestoresAndRemoves(t *testing.T) {
	s, root := testStore(t)
	oldFile := filepath.Join(root, "daemon.json")
	newFile := filepath.Join(root, "proxy.conf")
	if err := os.WriteFile(oldFile, []byte("before"), 0o640); err != nil {
		t.Fatal(err)
	}
	p := testPlan(
		Action{ID: "docker.configure", Title: "configure", Reversible: true},
		Action{ID: "docker.proxy.set", Title: "proxy", Reversible: true},
	)
	j, err := s.Begin(p, map[string]bool{"docker": true}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Snapshot(j, []string{oldFile, newFile}, time.Now()); err != nil {
		t.Fatal(err)
	}
	j.Actions[0].Status = "applied"
	j.Actions[1].Status = "applied"
	if err := s.Save(j); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldFile, []byte("after"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newFile, []byte("created"), 0o644); err != nil {
		t.Fatal(err)
	}
	var restarted []string
	_, res, err := s.Rollback(j.ID, RollbackOptions{
		Now: time.Now(),
		Restart: func(svcs []string) error {
			restarted = append(restarted, svcs...)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(oldFile); string(got) != "before" {
		t.Fatalf("old file = %q", got)
	}
	if _, err := os.Stat(newFile); !os.IsNotExist(err) {
		t.Fatalf("new file should be removed: %v", err)
	}
	if strings.Join(restarted, ",") != "docker" {
		t.Fatalf("restarted = %v", restarted)
	}
	if res.Status != JournalRolledBack {
		t.Fatalf("status = %s", res.Status)
	}
	if _, _, err := s.Rollback(j.ID, RollbackOptions{}); err == nil {
		t.Fatal("double rollback must fail")
	}
}

func TestRollbackStopsOnIrreversibleWithoutForce(t *testing.T) {
	s, root := testStore(t)
	path := filepath.Join(root, "daemon.json")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := testPlan(Action{ID: "docker.install", Title: "install", Reversible: false})
	j, err := s.Begin(p, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Snapshot(j, []string{path}, time.Now()); err != nil {
		t.Fatal(err)
	}
	j.Actions[0].Status = "applied"
	_ = s.Save(j)
	_ = os.WriteFile(path, []byte("after"), 0o644)
	if _, _, err := s.Rollback(j.ID, RollbackOptions{}); err == nil {
		t.Fatal("expected refusal for irreversible action")
	}
	if got, _ := os.ReadFile(path); string(got) != "after" {
		t.Fatalf("refused rollback must not modify files, got %q", got)
	}
	if _, _, err := s.Rollback(j.ID, RollbackOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "before" {
		t.Fatalf("forced rollback did not restore: %q", got)
	}
}

func TestRollbackDryRunDoesNotWrite(t *testing.T) {
	s, root := testStore(t)
	path := filepath.Join(root, "daemon.json")
	_ = os.WriteFile(path, []byte("before"), 0o644)
	p := testPlan(Action{ID: "docker.configure", Title: "configure", Reversible: true})
	j, _ := s.Begin(p, nil, time.Now())
	_ = s.Snapshot(j, []string{path}, time.Now())
	j.Actions[0].Status = "applied"
	_ = s.Save(j)
	_ = os.WriteFile(path, []byte("after"), 0o644)
	_, res, err := s.Rollback(j.ID, RollbackOptions{DryRun: true, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "dry-run" || len(res.Restored) != 1 {
		t.Fatalf("result = %+v", res)
	}
	if got, _ := os.ReadFile(path); string(got) != "after" {
		t.Fatalf("dry-run modified file: %q", got)
	}
}

type fakeExecutor struct {
	writePath string
	failOn    string
	executed  []string
}

func (f *fakeExecutor) Execute(a Action) error {
	f.executed = append(f.executed, a.ID)
	if a.ID == f.failOn {
		return os.ErrPermission
	}
	if a.ID == "docker.configure" && f.writePath != "" {
		_ = os.MkdirAll(filepath.Dir(f.writePath), 0o755)
		return os.WriteFile(f.writePath, []byte("new"), 0o644)
	}
	return nil
}

func TestApplyAutoRollbackRestoresState(t *testing.T) {
	s, root := testStore(t)
	path := filepath.Join(root, "daemon.json")
	plan := testPlan(
		Action{ID: "docker.configure", Title: "configure", Reversible: true},
		Action{ID: "docker.proxy.set", Title: "proxy", Reversible: true},
	)
	ex := &fakeExecutor{writePath: path, failOn: "docker.proxy.set"}
	var restarted []string
	res, err := Apply(plan, ex, ApplyOptions{
		Store:        s,
		AutoRollback: true,
		Services:     map[string]bool{"docker": true},
		SnapshotPaths: func(a Action) []string {
			if a.ID == "docker.configure" {
				return []string{path}
			}
			return nil
		},
		Restart: func(svcs []string) error {
			restarted = append(restarted, svcs...)
			return nil
		},
		Out: func(string, ...any) {},
	})
	if err == nil {
		t.Fatal("expected failure")
	}
	if !res.RolledBack {
		t.Fatalf("expected auto rollback, result %+v err %v", res, err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("rollback should remove created file: %v", statErr)
	}
	if strings.Join(restarted, ",") != "docker" {
		t.Fatalf("restarted = %v", restarted)
	}
	j, err := s.Load(res.JournalID)
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != JournalRolledBack {
		t.Fatalf("journal status = %s", j.Status)
	}
}

func TestApplyKeepsJournalWhenNotAutoRollingBack(t *testing.T) {
	s, root := testStore(t)
	path := filepath.Join(root, "daemon.json")
	plan := testPlan(Action{ID: "docker.configure", Title: "configure", Reversible: true})
	ex := &fakeExecutor{writePath: path, failOn: "docker.configure"}
	res, err := Apply(plan, ex, ApplyOptions{
		Store: s,
		SnapshotPaths: func(Action) []string {
			return []string{path}
		},
		Out: func(string, ...any) {},
	})
	if err == nil || !strings.Contains(err.Error(), "mkbox rollback") {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.JournalID == "" {
		t.Fatal("journal id missing")
	}
	j, err := s.Load(res.JournalID)
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != JournalFailed || j.Actions[0].Status != "failed" {
		t.Fatalf("journal = %+v", j)
	}
}

func TestProfileFromConfig(t *testing.T) {
	cfg := &config.Config{}
	cfg.Registry.Mirrors = []string{"https://a.example"}
	cfg.Docker.LogDriver = "json-file"
	cfg.Proxy.HTTP = "http://user:pass@proxy:8080"
	p := ProfileFromConfig(cfg, ScopeAll)
	if len(p.Docker.Mirrors) != 1 || p.Docker.LogDriver != "json-file" {
		t.Fatalf("profile = %+v", p)
	}
	if p.Proxy.URL != "http://user:pass@proxy:8080" {
		t.Fatalf("proxy = %q", p.Proxy.URL)
	}
	// Mutating the profile must not mutate config.
	p.Docker.Mirrors[0] = "changed"
	if cfg.Registry.Mirrors[0] != "https://a.example" {
		t.Fatal("profile aliases config slice")
	}
}
