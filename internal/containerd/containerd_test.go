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

func TestEditConfigRejectsMissingSystemdCgroup(t *testing.T) {
	if _, err := EditConfig("version = 2\n", ""); err == nil {
		t.Fatal("expected an error when SystemdCgroup is absent")
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
