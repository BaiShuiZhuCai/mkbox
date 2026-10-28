package coexist

import (
	"strings"
	"testing"
)

func TestParseContainerdConfig(t *testing.T) {
	content := `
[plugins."io.containerd.grpc.v1.cri"]
  sandbox_image = "registry.k8s.io/pause:3.10"
  [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runc.options]
    SystemdCgroup = true
  [plugins."io.containerd.grpc.v1.cri".registry]
    config_path = "/etc/containerd/certs.d"
`
	systemd, path, sandbox := ParseContainerdConfig(content)
	if !systemd {
		t.Fatal("SystemdCgroup should be true")
	}
	if path != "/etc/containerd/certs.d" {
		t.Fatalf("config_path = %q", path)
	}
	if sandbox != "registry.k8s.io/pause:3.10" {
		t.Fatalf("sandbox = %q", sandbox)
	}
}

func TestBuildPlanGreenfield(t *testing.T) {
	p := BuildPlan(Facts{CgroupVersion: 2, Ports: map[int]bool{}})
	if p.Topology != "containerd" {
		t.Fatalf("topology = %q", p.Topology)
	}
	if p.HasErrors() {
		t.Fatalf("unexpected errors: %#v", p.Findings)
	}
}

func TestBuildPlanSharedContainerd(t *testing.T) {
	p := BuildPlan(Facts{
		CgroupVersion:           2,
		DockerInstalled:         true,
		DockerRunning:           true,
		DockerCgroupDriver:      "systemd",
		ContainerdInstalled:     true,
		ContainerdRunning:       true,
		ContainerdSystemdCgroup: true,
		KubeletInstalled:        true,
		KubeletRunning:          true,
		KubeadmInstalled:        true,
		Ports:                   map[int]bool{},
	})
	if p.Topology != "shared-containerd" {
		t.Fatalf("topology = %q", p.Topology)
	}
	if p.HasErrors() {
		t.Fatalf("unexpected errors: %#v", p.Findings)
	}
	var found bool
	for _, f := range p.Findings {
		if f.Code == "restart-blast-radius" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected restart blast radius warning")
	}
}

func TestBuildPlanCgroupMismatch(t *testing.T) {
	p := BuildPlan(Facts{
		CgroupVersion:           2,
		DockerInstalled:         true,
		DockerCgroupDriver:      "cgroupfs",
		ContainerdInstalled:     true,
		ContainerdSystemdCgroup: false,
		Ports:                   map[int]bool{},
	})
	if !p.HasErrors() {
		t.Fatal("expected cgroup errors")
	}
	var codes []string
	for _, f := range p.Findings {
		if f.Severity == SeverityError {
			codes = append(codes, f.Code)
		}
	}
	joined := strings.Join(codes, ",")
	if !strings.Contains(joined, "docker-cgroup-driver") || !strings.Contains(joined, "containerd-systemd-cgroup") {
		t.Fatalf("missing cgroup findings: %s", joined)
	}
}

func TestBuildPlanExistingClusterAndInsecureAPI(t *testing.T) {
	p := BuildPlan(Facts{
		ClusterKubeconfig: "/home/u/.kube/config",
		DockerInstalled:   true,
		KubeletInstalled:  true,
		KubeletRunning:    true,
		Ports:             map[int]bool{2375: true, 6443: true},
		CgroupVersion:     2,
	})
	if p.Topology != "existing-cluster" {
		t.Fatalf("topology = %q", p.Topology)
	}
	if !p.HasErrors() {
		t.Fatal("plaintext Docker API should be an error")
	}
	var saw2375 bool
	for _, f := range p.Findings {
		if f.Code == "docker-api-plaintext" {
			saw2375 = true
		}
	}
	if !saw2375 {
		t.Fatal("expected plaintext Docker API finding")
	}
}

func TestBuildPlanCriDockerd(t *testing.T) {
	p := BuildPlan(Facts{
		DockerInstalled:     true,
		DockerRunning:       true,
		CriDockerdInstalled: true,
		CriDockerdRunning:   true,
		Ports:               map[int]bool{},
	})
	if p.Topology != "cri-dockerd" {
		t.Fatalf("topology = %q", p.Topology)
	}
}

func TestFormatPorts(t *testing.T) {
	if got := FormatPorts(map[int]bool{6443: true, 2375: true, 10250: false}); got != "2375, 6443" {
		t.Fatalf("FormatPorts = %q", got)
	}
}
