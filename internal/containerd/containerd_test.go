package containerd

import (
	"strings"
	"testing"
)

const sampleConfig = `version = 2
[plugins."io.containerd.grpc.v1.cri"]
  sandbox_image = "registry.k8s.io/pause:3.9"
  [plugins."io.containerd.grpc.v1.cri".containerd]
    [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runc]
      [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runc.options]
        SystemdCgroup = false
`

// minimalConfig mirrors the config.toml shipped by the containerd.io
// packages: no CRI plugin tables at all.
const minimalConfig = `version = 2
oom_score = 0

[grpc]
  address = "/run/containerd/containerd.sock"

[plugins]
  [plugins."io.containerd.internal.v1.opt"]
    path = "/opt/containerd"
`

// containerd2Config mirrors the split CRI plugin layout of containerd 2.x,
// which also quotes strings with single quotes by default.
const containerd2Config = `version = 3

[plugins.'io.containerd.cri.v1.images']
  snapshotter = 'overlayfs'

[plugins.'io.containerd.cri.v1.runtime']
  enable_selinux = false

[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.runc]
  runtime_type = 'io.containerd.runc.v2'
`

func TestEditConfig(t *testing.T) {
	out, err := EditConfig(sampleConfig, "registry.aliyuncs.com/google_containers")
	if err != nil {
		t.Fatalf("EditConfig: %v", err)
	}
	if !strings.Contains(out, "SystemdCgroup = true") {
		t.Fatalf("SystemdCgroup not enabled:\n%s", out)
	}
	if strings.Contains(out, "SystemdCgroup = false") {
		t.Fatalf("old SystemdCgroup setting still present:\n%s", out)
	}
	if !strings.Contains(out, `sandbox_image = "registry.aliyuncs.com/google_containers/pause:3.9"`) {
		t.Fatalf("sandbox image not rewritten:\n%s", out)
	}
	if !strings.Contains(out, `config_path = "/etc/containerd/certs.d"`) {
		t.Fatalf("registry config_path missing:\n%s", out)
	}
	if !strings.Contains(out, `[plugins."io.containerd.grpc.v1.cri".registry]`) {
		t.Fatalf("registry section missing:\n%s", out)
	}
}

func TestEditConfigAddsSectionsToMinimalConfig(t *testing.T) {
	out, err := EditConfig(minimalConfig, "registry.aliyuncs.com/google_containers")
	if err != nil {
		t.Fatalf("EditConfig: %v", err)
	}
	sandboxTable := strings.Index(out, `[plugins."io.containerd.grpc.v1.cri"]`)
	optionsTable := strings.Index(out, `[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runc.options]`)
	if sandboxTable < 0 || optionsTable < 0 {
		t.Fatalf("CRI tables not appended:\n%s", out)
	}
	if sandboxTable > optionsTable {
		t.Fatalf("sandbox table must precede the runc options table:\n%s", out)
	}
	if !strings.Contains(out, "SystemdCgroup = true") {
		t.Fatalf("SystemdCgroup not enabled:\n%s", out)
	}
	if !strings.Contains(out, `sandbox_image = "registry.aliyuncs.com/google_containers/pause:3.10"`) {
		t.Fatalf("sandbox image not written:\n%s", out)
	}
	if strings.Count(out, "SystemdCgroup") != 1 || strings.Count(out, "sandbox_image") != 1 {
		t.Fatalf("duplicated settings:\n%s", out)
	}
}

func TestEditConfigAdaptsToContainerd2Layout(t *testing.T) {
	out, err := EditConfig(containerd2Config, "")
	if err != nil {
		t.Fatalf("EditConfig: %v", err)
	}
	if strings.Contains(out, "io.containerd.grpc.v1.cri") {
		t.Fatalf("1.x CRI plugin written into a 2.x config:\n%s", out)
	}
	if !strings.Contains(out, `[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.runc.options]`) {
		t.Fatalf("2.x runc options table missing:\n%s", out)
	}
	runtimeTable := strings.Index(out, `[plugins.'io.containerd.cri.v1.runtime']`)
	sandbox := strings.Index(out, "sandbox_image = ")
	runc := strings.Index(out, `[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.runc]`)
	if runtimeTable < 0 || sandbox < 0 || runc < 0 || sandbox < runtimeTable || sandbox > runc {
		t.Fatalf("sandbox image not inserted under the runtime table:\n%s", out)
	}
	if !strings.Contains(out, `sandbox_image = "registry.k8s.io/pause:3.10"`) {
		t.Fatalf("sandbox image not written:\n%s", out)
	}
	if !strings.Contains(out, `runtime_type = 'io.containerd.runc.v2'`) {
		t.Fatalf("existing runc settings lost:\n%s", out)
	}
	if !strings.Contains(out, `[plugins.'io.containerd.cri.v1.images'.registry]`) {
		t.Fatalf("2.x registry section missing:\n%s", out)
	}
}

func TestEditConfigForVersionPicksLayoutForBareConfig(t *testing.T) {
	out, err := EditConfigForVersion(minimalConfig, "", 2)
	if err != nil {
		t.Fatalf("EditConfigForVersion: %v", err)
	}
	if !strings.Contains(out, `[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.runc.options]`) {
		t.Fatalf("containerd 2.x runc options table missing:\n%s", out)
	}
	if strings.Contains(out, "io.containerd.grpc.v1.cri") {
		t.Fatalf("1.x tables written for containerd 2.x:\n%s", out)
	}
}

func TestEditConfigFillsExistingTablesWithoutDuplicatingHeaders(t *testing.T) {
	content := `version = 2
[plugins."io.containerd.grpc.v1.cri"]
  sandbox_image = 'registry.k8s.io/pause:3.9'
  [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runc.options]
    BinaryName = "/usr/bin/runc"
`
	out, err := EditConfig(content, "")
	if err != nil {
		t.Fatalf("EditConfig: %v", err)
	}
	if strings.Count(out, "runtimes.runc.options]") != 1 {
		t.Fatalf("runc options table duplicated:\n%s", out)
	}
	header := strings.Index(out, "runtimes.runc.options]")
	cgroup := strings.Index(out, "SystemdCgroup = true")
	binary := strings.Index(out, `BinaryName = "/usr/bin/runc"`)
	if header < 0 || cgroup < 0 || binary < 0 || cgroup < header || cgroup > binary {
		t.Fatalf("SystemdCgroup not inserted into the existing table:\n%s", out)
	}
	if !strings.Contains(out, `sandbox_image = "registry.k8s.io/pause:3.9"`) {
		t.Fatalf("single-quoted sandbox image not normalised:\n%s", out)
	}
	if strings.Count(out, "sandbox_image") != 1 {
		t.Fatalf("sandbox image duplicated:\n%s", out)
	}
}

func TestEditConfigIsIdempotent(t *testing.T) {
	first, err := EditConfig(minimalConfig, "registry.aliyuncs.com/google_containers")
	if err != nil {
		t.Fatal(err)
	}
	second, err := EditConfig(first, "registry.aliyuncs.com/google_containers")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("EditConfig is not idempotent:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

func TestEditConfigEnablesPackagedCRIConfig(t *testing.T) {
	// deb/rpm packages ship CRI disabled, which makes kubeadm fail with
	// "unknown service runtime.v1.RuntimeService".
	content := "version = 2\ndisabled_plugins = [\"cri\"]\n\n[grpc]\n  address = \"/run/containerd/containerd.sock\"\n"
	out, err := EditConfig(content, "")
	if err != nil {
		t.Fatalf("EditConfig: %v", err)
	}
	if !strings.Contains(out, "disabled_plugins = []") {
		t.Fatalf("CRI plugin still disabled:\n%s", out)
	}
	if !strings.Contains(out, `[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runc.options]`) {
		t.Fatalf("runc options table missing:\n%s", out)
	}
	if DisablesCRI(out) {
		t.Fatalf("CRI still reported as disabled:\n%s", out)
	}
}

func TestEnableCRIPluginKeepsOtherPlugins(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
		changed bool
	}{
		{
			name:    "single entry",
			content: "version = 2\ndisabled_plugins = [\"cri\"]\n",
			want:    "disabled_plugins = []",
			changed: true,
		},
		{
			name:    "keeps unrelated plugins",
			content: "version = 2\ndisabled_plugins = [\"aufs\", \"cri\"]\n",
			want:    "disabled_plugins = [\"aufs\"]",
			changed: true,
		},
		{
			name:    "multi line array",
			content: "version = 2\ndisabled_plugins = [\n  \"cri\",\n  \"aufs\",\n]\n",
			want:    "disabled_plugins = [\"aufs\"]",
			changed: true,
		},
		{
			name:    "full plugin id",
			content: "version = 3\ndisabled_plugins = [\"io.containerd.cri.v1.runtime\"]\n",
			want:    "disabled_plugins = []",
			changed: true,
		},
		{
			name:    "already enabled",
			content: "version = 2\ndisabled_plugins = [\"aufs\"]\n",
			want:    "disabled_plugins = [\"aufs\"]",
			changed: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, changed := EnableCRIPlugin(tc.content)
			if !strings.Contains(out, tc.want) {
				t.Fatalf("missing %q:\n%s", tc.want, out)
			}
			if strings.Contains(out, `"cri"`) {
				t.Fatalf("CRI entry still present:\n%s", out)
			}
			if changed != tc.changed {
				t.Fatalf("changed = %v; want %v", changed, tc.changed)
			}
			if !tc.changed && out != tc.content {
				t.Fatalf("disabled config must be returned unchanged:\n%s", out)
			}
		})
	}
}

func TestEnableCRIPluginIgnoresNestedKey(t *testing.T) {
	// Inside a table the key would be <table>.disabled_plugins, so it must be
	// left untouched.
	content := "version = 2\n[plugins.\"io.containerd.internal.v1.opt\"]\n  disabled_plugins = [\"cri\"]\n"
	out, changed := EnableCRIPlugin(content)
	if changed || out != content {
		t.Fatalf("nested disabled_plugins must be left alone:\n%s", out)
	}
}

func TestEditConfigEnablesCRIAndUsesContainerd2Tables(t *testing.T) {
	content := "version = 3\ndisabled_plugins = [\"cri\"]\n\n" + strings.TrimPrefix(containerd2Config, "version = 3\n\n")
	out, err := EditConfig(content, "registry.aliyuncs.com/google_containers")
	if err != nil {
		t.Fatalf("EditConfig: %v", err)
	}
	if !strings.Contains(out, "disabled_plugins = []") || DisablesCRI(out) {
		t.Fatalf("CRI plugin still disabled:\n%s", out)
	}
	if strings.Contains(out, "io.containerd.grpc.v1.cri") {
		t.Fatalf("1.x CRI plugin written into a 2.x config:\n%s", out)
	}
	if !strings.Contains(out, `[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.runc.options]`) {
		t.Fatalf("2.x runc options table missing:\n%s", out)
	}
}

func TestCRIStatus(t *testing.T) {
	// ctr plugins ls prints the plugin URI split over the TYPE and ID
	// columns, so the uri never appears on a single line.
	const header = "TYPE                                  ID                        PLATFORMS      STATUS\n"
	cases := []struct {
		name   string
		out    string
		found  bool
		status string
	}{
		{
			name:   "containerd 1.x",
			out:    header + "io.containerd.grpc.v1                 healthcheck               -              ok\nio.containerd.grpc.v1                 cri                       linux/amd64    ok\n",
			found:  true,
			status: "ok",
		},
		{
			name:   "containerd 2.x",
			out:    header + "io.containerd.cri.v1                  images                    linux/amd64    ok\nio.containerd.cri.v1                  runtime                   linux/amd64    ok\n",
			found:  true,
			status: "ok",
		},
		{
			name:   "cri plugin failed",
			out:    header + "io.containerd.cri.v1                  runtime                   linux/amd64    error\n",
			found:  true,
			status: "error",
		},
		{
			name:  "cri disabled",
			out:   header + "io.containerd.grpc.v1                 healthcheck               -              ok\n",
			found: false,
		},
		{
			name:   "quiet output",
			out:    "io.containerd.grpc.v1.cri\n",
			found:  true,
			status: "",
		},
		{
			name:  "empty",
			out:   "",
			found: false,
		},
	}
	for _, tc := range cases {
		found, status := CRIStatus(tc.out)
		if found != tc.found || status != tc.status {
			t.Errorf("%s: CRIStatus = (%v, %q); want (%v, %q)", tc.name, found, status, tc.found, tc.status)
		}
	}
}

func TestParseMajorVersion(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"containerd github.com/containerd/containerd v1.7.20 8fc6bcff", 1},
		{"containerd github.com/containerd/containerd/v2 v2.1.3 1c9d7f4", 2},
		{"containerd github.com/containerd/containerd v1.6.32", 1},
		{"", 0},
		{"garbage", 0},
	}
	for _, tc := range cases {
		if got := ParseMajorVersion(tc.in); got != tc.want {
			t.Errorf("ParseMajorVersion(%q) = %d; want %d", tc.in, got, tc.want)
		}
	}
}

func TestEditConfigPreservesPauseVersionAndDefaultRepo(t *testing.T) {
	out, err := EditConfig(sampleConfig, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `sandbox_image = "registry.k8s.io/pause:3.9"`) {
		t.Fatalf("default repo should keep pause version:\n%s", out)
	}
	if !strings.Contains(out, "SystemdCgroup = true") {
		t.Fatalf("SystemdCgroup not enabled:\n%s", out)
	}
}

func TestEnsureRegistryConfigPathReplacesExisting(t *testing.T) {
	content := `version = 2
[plugins."io.containerd.grpc.v1.cri".registry]
  config_path = "/old/path"
`
	out := EnsureRegistryConfigPath(content, "/etc/containerd/certs.d")
	if !strings.Contains(out, `config_path = "/etc/containerd/certs.d"`) {
		t.Fatalf("config_path not replaced:\n%s", out)
	}
	if strings.Contains(out, "/old/path") {
		t.Fatalf("old config_path still present:\n%s", out)
	}
}

func TestEnsureRegistryConfigPathAddsToExistingSection(t *testing.T) {
	content := `version = 2
[plugins."io.containerd.grpc.v1.cri".registry]
  [plugins."io.containerd.grpc.v1.cri".registry.mirrors]
`
	out := EnsureRegistryConfigPath(content, "/etc/containerd/certs.d")
	if !strings.Contains(out, `config_path = "/etc/containerd/certs.d"`) {
		t.Fatalf("config_path not injected:\n%s", out)
	}
}

func TestEnsureRegistryConfigPathUsesNewCRISection(t *testing.T) {
	content := `version = 2
[plugins.'io.containerd.cri.v1.images'.registry]
  config_path = "/old"
`
	out := EnsureRegistryConfigPath(content, "/etc/containerd/certs.d")
	if strings.Count(out, "config_path") != 1 {
		t.Fatalf("expected one config_path:\n%s", out)
	}
	if !strings.Contains(out, `config_path = "/etc/containerd/certs.d"`) {
		t.Fatalf("config_path not replaced in new CRI section:\n%s", out)
	}
}

func TestHostsTOML(t *testing.T) {
	out := HostsTOML("https://docker.io", []string{
		"https://mirror.example.com/",
		"https://mirror.example.com",
		"https://other.example.com",
	})
	if !strings.Contains(out, `server = "https://docker.io"`) {
		t.Fatalf("server missing:\n%s", out)
	}
	if strings.Count(out, "[host.") != 2 {
		t.Fatalf("mirrors not deduplicated:\n%s", out)
	}
	if !strings.Contains(out, `[host."https://mirror.example.com"]`) {
		t.Fatalf("mirror host missing:\n%s", out)
	}
}
