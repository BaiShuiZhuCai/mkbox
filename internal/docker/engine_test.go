package docker

import (
	"fmt"
	"strings"
	"testing"

	"github.com/makebox/makebox/internal/system"
)

func TestRPMDistro(t *testing.T) {
	cases := []struct {
		id   string
		like []string
		want string
	}{
		{"fedora", nil, "fedora"},
		{"fedora", []string{"rhel", "centos"}, "fedora"},
		{"centos", []string{"rhel", "centos", "fedora"}, "centos"},
		{"rhel", []string{"fedora"}, "centos"},
		{"rhel", nil, "centos"},
		{"centos", []string{"rhel", "centos"}, "centos"},
		{"rocky", []string{"rhel", "centos", "fedora"}, "centos"},
		{"almalinux", []string{"rhel", "centos", "fedora"}, "centos"},
		{"ol", []string{"fedora"}, "centos"},
		{"", []string{"fedora"}, "fedora"},
		{"", []string{"rhel", "fedora"}, "fedora"},
		{"", []string{"rhel"}, "centos"},
	}
	for _, c := range cases {
		got := rpmDistro(&system.OS{ID: c.id, IDLike: c.like})
		if got != c.want {
			t.Errorf("rpmDistro(%s, %v) = %q; want %q", c.id, c.like, got, c.want)
		}
	}
}

func TestRPMArch(t *testing.T) {
	for in, want := range map[string]string{
		"amd64": "x86_64",
		"arm64": "aarch64",
		"386":   "i686",
		"arm":   "armhfp",
	} {
		if got := rpmArch(in); got != want {
			t.Errorf("rpmArch(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestReleaseVersions(t *testing.T) {
	if got := strings.Join(releaseVersions("43"), ","); got != "43,42,41" {
		t.Errorf("releaseVersions(43) = %q", got)
	}
	if got := strings.Join(releaseVersions("9.4"), ","); got != "9,8,7" {
		t.Errorf("releaseVersions(9.4) = %q", got)
	}
	if got := strings.Join(releaseVersions("2"), ","); got != "2,1" {
		t.Errorf("releaseVersions(2) = %q", got)
	}
	for _, in := range []string{"", "rawhide", "v43"} {
		if got := releaseVersions(in); got != nil {
			t.Errorf("releaseVersions(%q) = %v; want nil", in, got)
		}
	}
}

func TestFedoraRepoPath(t *testing.T) {
	osInfo := &system.OS{ID: "fedora", VersionID: "43", Family: "rhel"}
	distro := rpmDistro(osInfo)
	got := fmt.Sprintf("%s/linux/%s/docker-ce.repo", mirrorHost("aliyun"), distro)
	want := "https://mirrors.aliyun.com/docker-ce/linux/fedora/docker-ce.repo"
	if got != want {
		t.Errorf("fedora repo url = %q; want %q", got, want)
	}
}

func TestCentOS7RepoPath(t *testing.T) {
	osInfo := &system.OS{ID: "centos", IDLike: []string{"rhel", "fedora"}, VersionID: "7", Family: "rhel"}
	got := fmt.Sprintf("%s/linux/%s/docker-ce.repo", mirrorHost("aliyun"), rpmDistro(osInfo))
	want := "https://mirrors.aliyun.com/docker-ce/linux/centos/docker-ce.repo"
	if got != want {
		t.Errorf("centos 7 repo url = %q; want %q", got, want)
	}
	if vers := releaseVersions(osInfo.VersionID); strings.Join(vers, "/") != "7/6/5" {
		t.Errorf("releaseVersions(7) = %v; want 7/6/5", vers)
	}
}
