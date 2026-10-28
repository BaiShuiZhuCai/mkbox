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
		{"rhel", []string{"fedora"}, "fedora"},
		{"rhel", nil, "centos"},
		{"centos", []string{"rhel", "centos"}, "centos"},
		{"rocky", []string{"rhel", "centos"}, "centos"},
		{"almalinux", []string{"rhel", "centos"}, "centos"},
		{"ol", []string{"fedora"}, "fedora"},
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

// Fedora must not reuse the CentOS repository path: dnf expands $releasever
// to the Fedora release, which only exists below linux/fedora.
func TestFedoraRepoPath(t *testing.T) {
	osInfo := &system.OS{ID: "fedora", VersionID: "43", Family: "rhel"}
	distro := rpmDistro(osInfo)
	got := fmt.Sprintf("%s/linux/%s/docker-ce.repo", mirrorHost("aliyun"), distro)
	want := "https://mirrors.aliyun.com/docker-ce/linux/fedora/docker-ce.repo"
	if got != want {
		t.Errorf("fedora repo url = %q; want %q", got, want)
	}
}
