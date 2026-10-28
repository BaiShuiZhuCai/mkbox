package k8s

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/makebox/makebox/internal/execx"
	"github.com/makebox/makebox/internal/system"
)

func TestKubeadmAPIVersion(t *testing.T) {
	tests := map[string]string{
		"1.30.5":  "v1beta3",
		"v1.29.0": "v1beta3",
		"1.31.0":  "v1beta4",
		"v1.35.2": "v1beta4",
		"1.35":    "v1beta4",
		"garbage": "v1beta4",
		"":        "v1beta4",
	}
	for in, want := range tests {
		if got := KubeadmAPIVersion(in); got != want {
			t.Errorf("KubeadmAPIVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeEndpoint(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "", false},
		{"10.0.0.1", "10.0.0.1:6443", false},
		{"example.com", "example.com:6443", false},
		{"example.com:6443", "example.com:6443", false},
		{"example.com:8443", "example.com:8443", false},
		{"fd00::1", "[fd00::1]:6443", false},
		{"example.com:abc", "", true},
		{"example.com:0", "", true},
		{"example.com:99999", "", true},
		{"bad:host:port", "", true},
		{"bad endpoint", "", true},
		{"bad_host:6443", "", true},
	}
	for _, tc := range tests {
		got, err := NormalizeEndpoint(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("NormalizeEndpoint(%q) expected error, got %q", tc.in, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("NormalizeEndpoint(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestKubeadmConfig(t *testing.T) {
	opts := KubeadmOptions{
		APIAddress:      "10.0.0.10",
		ImageRepository: "registry.aliyuncs.com/google_containers/",
		PodCIDR:         "10.244.0.0/16",
		ServiceCIDR:     "10.96.0.0/12",
		CRISocket:       "unix:///run/containerd/containerd.sock",
		APIVersion:      "v1beta4",
	}
	out, err := KubeadmConfig(opts)
	if err != nil {
		t.Fatalf("KubeadmConfig: %v", err)
	}
	for _, want := range []string{
		"apiVersion: kubeadm.k8s.io/v1beta4",
		"kind: InitConfiguration",
		`advertiseAddress: "10.0.0.10"`,
		"kind: ClusterConfiguration",
		`imageRepository: "registry.aliyuncs.com/google_containers"`,
		`podSubnet: "10.244.0.0/16"`,
		`serviceSubnet: "10.96.0.0/12"`,
		"- name: node-ip",
		`value: "10.0.0.10"`,
		"certSANs:",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("kubeadm config missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "/\"") {
		t.Fatalf("image repository trailing slash should be trimmed:\n%s", out)
	}

	opts.APIVersion = "v1beta3"
	out, err = KubeadmConfig(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `node-ip: "10.0.0.10"`) {
		t.Fatalf("v1beta3 should use map-style kubeletExtraArgs:\n%s", out)
	}
	if strings.Contains(out, "- name: node-ip") {
		t.Fatalf("v1beta3 must not use list-style kubeletExtraArgs:\n%s", out)
	}
}

func TestKubeadmConfigValidation(t *testing.T) {
	base := KubeadmOptions{
		APIAddress:      "10.0.0.10",
		ImageRepository: "registry.k8s.io",
		PodCIDR:         "10.244.0.0/16",
		ServiceCIDR:     "10.96.0.0/12",
	}
	cases := []func(o *KubeadmOptions){
		func(o *KubeadmOptions) { o.APIAddress = "not-an-ip" },
		func(o *KubeadmOptions) { o.PodCIDR = "bad" },
		func(o *KubeadmOptions) { o.ServiceCIDR = "" },
		func(o *KubeadmOptions) { o.ImageRepository = "" },
	}
	for i, mutate := range cases {
		o := base
		mutate(&o)
		if _, err := KubeadmConfig(o); err == nil {
			t.Errorf("case %d: expected validation error", i)
		}
	}
}

func TestReleaseSeriesAndRepoBase(t *testing.T) {
	for in, want := range map[string]string{
		"v1.35.2":       "1.35",
		"1.31":          "1.31",
		"v1.28.15+k3s1": "1.28",
		"  1.30.0  ":    "1.30",
	} {
		got, err := ReleaseSeries(in)
		if err != nil || got != want {
			t.Errorf("ReleaseSeries(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ReleaseSeries("v2.0.0"); err == nil {
		t.Error("expected invalid series error")
	}
	if got := K8sRepoBase("1.35", "aliyun", "deb"); got != "https://mirrors.aliyun.com/kubernetes-new/core/stable/v1.35/deb" {
		t.Errorf("unexpected aliyun repo: %s", got)
	}
	if got := K8sRepoBase("1.35", "official", "rpm"); got != "https://pkgs.k8s.io/core:/stable:/v1.35/rpm" {
		t.Errorf("unexpected official repo: %s", got)
	}
}

func TestK3sRegistriesYAML(t *testing.T) {
	out := K3sRegistriesYAML([]string{
		"https://docker.m.daocloud.io",
		"https://docker.m.daocloud.io/",
		"https://docker.io",
	})
	if !strings.Contains(out, "mirrors:") || !strings.Contains(out, "docker.io:") {
		t.Fatalf("registries yaml missing mirror section:\n%s", out)
	}
	if strings.Count(out, "https://docker.m.daocloud.io") != 1 {
		t.Fatalf("mirror not deduplicated:\n%s", out)
	}
	if !strings.Contains(out, `- "https://docker.io"`) {
		t.Fatalf("official fallback missing:\n%s", out)
	}
	if out := K3sRegistriesYAML(nil); strings.Contains(out, "mirrors:") {
		t.Fatalf("empty mirrors should only yield a header:\n%s", out)
	}
}

func TestPatchMetricsServer(t *testing.T) {
	manifest := `      containers:
        - name: metrics-server
          args:
            - --cert-dir=/tmp
            - --kubelet-preferred-address-types=InternalIP
`
	out := PatchMetricsServer(manifest)
	if !strings.Contains(out, "--kubelet-insecure-tls") {
		t.Fatalf("patch not applied:\n%s", out)
	}
	if strings.Count(out, "--kubelet-insecure-tls") != 1 {
		t.Fatalf("patch applied more than once:\n%s", out)
	}
	if again := PatchMetricsServer(out); again != out {
		t.Fatal("patch should be idempotent")
	}
	unchanged := PatchMetricsServer("no args here")
	if unchanged != "no args here" {
		t.Fatal("manifest without known args should be unchanged")
	}
}

func TestPatchFlannelCIDR(t *testing.T) {
	manifest := `  net-conf.json: |
    {
      "Network": "10.244.0.0/16",
      "Backend": {
        "Type": "vxlan"
      }
    }
`
	out, ok := PatchFlannelCIDR(manifest, "10.245.0.0/16")
	if !ok || !strings.Contains(out, `"Network": "10.245.0.0/16"`) {
		t.Fatalf("flannel CIDR not patched (ok=%v):\n%s", ok, out)
	}
	if strings.Contains(out, "10.244.0.0/16") {
		t.Fatalf("old flannel CIDR remains:\n%s", out)
	}
	if out, ok := PatchFlannelCIDR("no net-conf here", "10.245.0.0/16"); ok || out != "no net-conf here" {
		t.Fatalf("manifest without Network should be unchanged (ok=%v):\n%s", ok, out)
	}
}

func TestPatchCalicoCIDR(t *testing.T) {
	commented := `          # - name: CALICO_IPV4POOL_CIDR
          #   value: "192.168.0.0/16"
`
	out, ok := PatchCalicoCIDR(commented, "10.244.0.0/16")
	if !ok || !strings.Contains(out, `value: "10.244.0.0/16"`) {
		t.Fatalf("commented CIDR not patched (ok=%v):\n%s", ok, out)
	}
	if strings.Contains(out, "# - name: CALICO_IPV4POOL_CIDR") {
		t.Fatalf("commented block should be uncommented:\n%s", out)
	}

	active := `          - name: CALICO_IPV4POOL_CIDR
            value: "192.168.0.0/16"
`
	out, ok = PatchCalicoCIDR(active, "10.244.0.0/16")
	if !ok || !strings.Contains(out, `value: "10.244.0.0/16"`) {
		t.Fatalf("active CIDR not patched (ok=%v):\n%s", ok, out)
	}

	out, ok = PatchCalicoCIDR("manifest without CIDR", "192.168.0.0/16")
	if !ok || out != "manifest without CIDR" {
		t.Fatalf("default CIDR should be a no-op (ok=%v): %s", ok, out)
	}
	if _, ok := PatchCalicoCIDR("manifest without CIDR", "10.244.0.0/16"); ok {
		t.Fatal("non-default CIDR without a patch target should report false")
	}
}

func TestPreflightHelpers(t *testing.T) {
	fstab := `# /etc/fstab
UUID=abc / ext4 defaults 0 1
/swapfile none swap sw 0 0
  /dev/sdb2 none swap defaults 0 0
`
	out, n := CommentSwap(fstab)
	if n != 2 {
		t.Fatalf("expected 2 swap entries, got %d:\n%s", n, out)
	}
	if strings.Contains(out, "\n/swapfile none swap") || strings.Contains(out, "\n  /dev/sdb2 none swap") {
		t.Fatalf("active swap entries remain:\n%s", out)
	}
	if !strings.Contains(out, "# disabled by mkbox: /swapfile none swap sw 0 0") {
		t.Fatalf("swap comment missing:\n%s", out)
	}

	calico := RequiredPorts("calico", false)
	if !containsPort(calico, "179/tcp") || !containsPort(calico, "4789/udp") {
		t.Fatalf("calico ports incomplete: %v", calico)
	}
	flannel := RequiredPorts("flannel", false)
	if !containsPort(flannel, "8472/udp") || containsPort(flannel, "179/tcp") {
		t.Fatalf("non-calico ports wrong: %v", flannel)
	}
	k3s := RequiredPorts("flannel", true)
	if !containsPort(k3s, "51820/udp") {
		t.Fatalf("k3s ports missing wireguard: %v", k3s)
	}

	if got := UFWPort("30000-32767/tcp"); got != "30000:32767/tcp" {
		t.Fatalf("UFWPort range = %q", got)
	}
	if got := UFWPort("6443/tcp"); got != "6443/tcp" {
		t.Fatalf("UFWPort single = %q", got)
	}

	selinux := "SELINUX=enforcing\nSELINUXTYPE=targeted\n"
	out2, changed := SetSELinuxPermissive(selinux)
	if !changed || !strings.Contains(out2, "SELINUX=permissive") {
		t.Fatalf("SELinux not switched: %q", out2)
	}
	if _, changed := SetSELinuxPermissive("SELINUX=permissive\n"); changed {
		t.Fatal("already permissive should not report a change")
	}
}

func TestNormalizeServerURL(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "", false},
		{"10.0.0.1", "https://10.0.0.1:6443", false},
		{"https://api.example.com", "https://api.example.com:6443", false},
		{"https://api.example.com:8443", "https://api.example.com:8443", false},
		{"http://api.example.com", "http://api.example.com:6443", false},
		{"://bad", "", true},
	}
	for _, tc := range tests {
		got, err := normalizeServerURL(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("normalizeServerURL(%q) expected error, got %q", tc.in, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("normalizeServerURL(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestNormalizeJoinServer(t *testing.T) {
	tests := map[string]string{
		"10.0.0.1":                      "10.0.0.1:6443",
		"https://10.0.0.1":              "10.0.0.1:6443",
		"https://10.0.0.1:6443":         "10.0.0.1:6443",
		"https://k8s.example.com:8443/": "k8s.example.com:8443",
		"fd00::1":                       "[fd00::1]:6443",
		"cp.example.com":                "cp.example.com:6443",
		"[::1]:6443":                    "[::1]:6443",
	}
	for in, want := range tests {
		got, err := normalizeJoinServer(in)
		if err != nil || got != want {
			t.Errorf("normalizeJoinServer(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{
		"", "https://example.com/path", "bad host", "host:99999", "host:0",
		"host:-1", "bad_host:6443", "user:pass@host:6443", "ftp://host:6443",
	} {
		if _, err := normalizeJoinServer(bad); err == nil {
			t.Errorf("normalizeJoinServer(%q) expected error", bad)
		}
	}
}

func TestValidateJoinCredentials(t *testing.T) {
	hash := "sha256:" + strings.Repeat("a", 64)
	key := strings.Repeat("b", 64)
	if err := validateJoinCredentials("abcdef.0123456789abcdef", hash, key, true); err != nil {
		t.Fatalf("valid credentials rejected: %v", err)
	}
	for _, token := range []string{"", "x", "ABCDEF.0123456789abcdef", "abcdef.0123456789abcde"} {
		if err := validateJoinCredentials(token, hash, "", false); err == nil {
			t.Errorf("token %q should be rejected", token)
		}
	}
	for _, ca := range []string{"", "sha256:bad", "md5:" + strings.Repeat("a", 64), "sha256:" + strings.Repeat("z", 64)} {
		if err := validateJoinCredentials("abcdef.0123456789abcdef", ca, "", false); err == nil {
			t.Errorf("CA hash %q should be rejected", ca)
		}
	}
	if err := validateJoinCredentials("abcdef.0123456789abcdef", hash, "short", true); err == nil {
		t.Fatal("invalid certificate key should be rejected")
	}
}

func TestValidateK3sJoin(t *testing.T) {
	got, err := validateK3sJoin("10.0.0.10", "token")
	if err != nil || got != "https://10.0.0.10:6443" {
		t.Fatalf("validateK3sJoin = %q, %v", got, err)
	}
	for _, tc := range []struct{ server, token string }{
		{"", "token"},
		{"10.0.0.10", ""},
		{"host:99999", "token"},
		{"bad_host", "token"},
		{"https://user:pass@host:6443", "token"},
		{"https://host:6443?x=1", "token"},
	} {
		if _, err := validateK3sJoin(tc.server, tc.token); err == nil {
			t.Errorf("validateK3sJoin(%q, %q) should be rejected", tc.server, tc.token)
		}
	}
}

func TestExportKubeconfig(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "admin.conf")
	raw := "apiVersion: v1\nclusters:\n- cluster:\n    server: https://127.0.0.1:6443\n  name: test\n"
	if err := os.WriteFile(source, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	fs := &system.FS{}
	out := filepath.Join(dir, "config")
	path, err := ExportKubeconfig(fs, source, out, "10.1.2.3", false)
	if err != nil || path != out {
		t.Fatalf("ExportKubeconfig = %q, %v", path, err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "server: https://10.1.2.3:6443") {
		t.Fatalf("server not rewritten:\n%s", data)
	}
	st, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("output mode = %v", st.Mode())
	}

	if _, err := ExportKubeconfig(fs, source, out, "10.9.8.7", false); err == nil {
		t.Fatal("existing output without --force should be rejected")
	}

	for _, server := range []string{
		"ftp://10.0.0.1",
		"https://10.0.0.1:99999",
		"https://user:pass@10.0.0.1:6443",
		"https://bad_host:6443",
	} {
		badOut := filepath.Join(dir, "bad.conf")
		if _, err := ExportKubeconfig(fs, source, badOut, server, true); err == nil {
			t.Errorf("server %q should be rejected", server)
		}
		if _, err := os.Stat(badOut); err == nil {
			t.Errorf("server %q must not produce output", server)
			_ = os.Remove(badOut)
		}
	}

	inPlace := filepath.Join(dir, "in-place.conf")
	if err := os.WriteFile(inPlace, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ExportKubeconfig(fs, inPlace, inPlace, "10.4.5.6", false); err == nil {
		t.Fatal("in-place rewrite without --force should be rejected")
	}
	if _, err := ExportKubeconfig(fs, inPlace, inPlace, "10.4.5.6", true); err != nil {
		t.Fatalf("in-place rewrite with --force: %v", err)
	}
	data, err = os.ReadFile(inPlace)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "server: https://10.4.5.6:6443") {
		t.Fatalf("in-place server not rewritten:\n%s", data)
	}
}

func TestAddonDryRunValidatesLocalSource(t *testing.T) {
	a := NewAddonInstaller(execx.New(true, false), "", "")
	dir := t.TempDir()
	if err := a.InstallSource("custom", filepath.Join(dir, "missing.yaml")); err == nil {
		t.Fatal("missing local manifest should be rejected")
	}
	empty := filepath.Join(dir, "empty.yaml")
	if err := os.WriteFile(empty, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.InstallSource("custom", empty); err == nil {
		t.Fatal("empty local manifest should be rejected")
	}
	good := filepath.Join(dir, "ok.yaml")
	if err := os.WriteFile(good, []byte("apiVersion: v1\nkind: ConfigMap\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.InstallSource("custom", good); err != nil {
		t.Fatalf("valid local manifest rejected: %v", err)
	}
	if err := a.InstallSource("custom", "://bad"); err == nil {
		t.Fatal("invalid remote manifest URL should be rejected")
	}
}

func TestJoinK3sDryRunWithoutSystemd(t *testing.T) {
	r := execx.New(true, false)
	fs := &system.FS{DryRun: true}
	osInfo := &system.OS{IsRoot: true}
	if err := JoinK3s(r, fs, osInfo, "10.0.0.10", "node-token", "", nil, false); err != nil {
		t.Fatalf("dry-run should not require systemd: %v", err)
	}
}

func containsPort(ports []string, want string) bool {
	for _, p := range ports {
		if p == want {
			return true
		}
	}
	return false
}
