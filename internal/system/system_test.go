package system

import (
	"reflect"
	"testing"
)

func TestParseOSRelease(t *testing.T) {
	content := `# comment
NAME="Ubuntu"
ID=ubuntu
ID_LIKE="debian"
VERSION_ID="22.04"
VERSION_CODENAME=jammy
PRETTY_NAME="Ubuntu 22.04.4 LTS"
QUOTED='single'
ESCAPED="a \"quote\""
`
	got := ParseOSRelease(content)
	want := map[string]string{
		"NAME":             "Ubuntu",
		"ID":               "ubuntu",
		"ID_LIKE":          "debian",
		"VERSION_ID":       "22.04",
		"VERSION_CODENAME": "jammy",
		"PRETTY_NAME":      "Ubuntu 22.04.4 LTS",
		"QUOTED":           "single",
		"ESCAPED":          `a "quote"`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseOSRelease = %#v, want %#v", got, want)
	}
	if got := ParseOSRelease("INVALID LINE\nKEY=value\n"); got["KEY"] != "value" {
		t.Fatalf("ParseOSRelease skipped valid key: %v", got)
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		id     string
		like   []string
		family string
		pkg    string
	}{
		{"ubuntu", []string{}, "debian", "apt"},
		{"debian", []string{}, "debian", "apt"},
		{"linuxmint", []string{"ubuntu", "debian"}, "debian", "apt"},
		{"rocky", []string{"rhel", "centos"}, "rhel", "dnf"},
		{"fedora", []string{}, "rhel", "dnf"},
		{"opensuse-leap", []string{"suse"}, "suse", "zypper"},
		{"arch", []string{}, "arch", "pacman"},
		{"manjaro", []string{"arch"}, "arch", "pacman"},
		{"alpine", []string{}, "alpine", "apk"},
		{"unknown-distro", []string{}, "unknown", ""},
	}
	for _, tc := range cases {
		family, pkg := Classify(tc.id, tc.like)
		if family != tc.family || pkg != tc.pkg {
			t.Errorf("Classify(%q, %v) = %q, %q; want %q, %q", tc.id, tc.like, family, pkg, tc.family, tc.pkg)
		}
	}
}

func TestBackupPath(t *testing.T) {
	path := BackupPath("/etc/hosts")
	if len(path) <= len("/etc/hosts") || path[:len("/etc/hosts")] != "/etc/hosts" {
		t.Fatalf("unexpected backup path: %s", path)
	}
	if path == "/etc/hosts" {
		t.Fatal("backup path must differ from original")
	}
}
