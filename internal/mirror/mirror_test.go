package mirror

import (
	"strings"
	"testing"
)

func TestGetSource(t *testing.T) {
	src, ok := GetSource("Aliyun")
	if !ok || src.ID != "aliyun" {
		t.Fatalf("GetSource(aliyun) = %+v, %v", src, ok)
	}
	if _, ok := GetSource("missing"); ok {
		t.Fatal("unknown source should not be found")
	}
}

func TestUbuntuSources(t *testing.T) {
	src, _ := GetSource("tuna")
	out, err := UbuntuSources("jammy", src)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "deb ") != 4 {
		t.Fatalf("expected 4 ubuntu suites:\n%s", out)
	}
	for _, suite := range []string{"jammy", "jammy-updates", "jammy-backports", "jammy-security"} {
		if !strings.Contains(out, suite) {
			t.Fatalf("suite %s missing:\n%s", suite, out)
		}
	}
	if !strings.Contains(out, "main restricted universe multiverse") {
		t.Fatalf("ubuntu components missing:\n%s", out)
	}
	if _, err := UbuntuSources("Jammy!", src); err == nil {
		t.Fatal("invalid codename should error")
	}
}

func TestUbuntuDEB822(t *testing.T) {
	src, _ := GetSource("ustc")
	out, err := UbuntuDEB822("noble", src)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Types: deb", "URIs: https://mirrors.ustc.edu.cn/ubuntu/", "Suites: noble noble-updates noble-backports noble-security"} {
		if !strings.Contains(out, want) {
			t.Fatalf("deb822 output missing %q:\n%s", want, out)
		}
	}
	if _, err := UbuntuDEB822("../evil", src); err == nil {
		t.Fatal("invalid codename should error")
	}
}

func TestDebianSources(t *testing.T) {
	src, _ := GetSource("huawei")
	out, err := DebianSources("bookworm", src, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "non-free-firmware") {
		t.Fatalf("firmware component missing:\n%s", out)
	}
	if !strings.Contains(out, "bookworm-security") {
		t.Fatalf("security suite missing:\n%s", out)
	}
	out, err = DebianSources("bullseye", src, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "non-free-firmware") {
		t.Fatalf("old Debian should not get non-free-firmware:\n%s", out)
	}
	if _, err := DebianSources("bad codename", src, false); err == nil {
		t.Fatal("invalid codename should error")
	}
}

func TestDebianDEB822(t *testing.T) {
	src, _ := GetSource("aliyun")
	out, err := DebianDEB822("trixie", src, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "Types: deb") != 2 {
		t.Fatalf("expected main and security stanzas:\n%s", out)
	}
	if !strings.Contains(out, "Suites: trixie trixie-updates trixie-backports") {
		t.Fatalf("main suites missing:\n%s", out)
	}
	if !strings.Contains(out, "Suites: trixie-security") {
		t.Fatalf("security suite missing:\n%s", out)
	}
}

func TestPrependArchMirror(t *testing.T) {
	original := "Server = https://geo.mirror.pkgbuild.com/$repo/os/$arch\n"
	out := PrependArchMirror(original, "https://mirrors.aliyun.com/archlinux/$repo/os/$arch")
	if !strings.HasPrefix(out, "# Managed by makebox (mkbox)\nServer = https://mirrors.aliyun.com/archlinux/$repo/os/$arch") {
		t.Fatalf("mirror not prepended:\n%s", out)
	}
	if again := PrependArchMirror(out, "https://mirrors.aliyun.com/archlinux/$repo/os/$arch"); again != out {
		t.Fatal("PrependArchMirror should be idempotent")
	}
}

func TestReplaceAlpineMirror(t *testing.T) {
	src, _ := GetSource("tuna")
	content := `https://dl-cdn.alpinelinux.org/alpine/v3.20/main
https://mirrors.aliyun.com/alpine/v3.20/community
`
	out := ReplaceAlpineMirror(content, src)
	if strings.Contains(out, "dl-cdn.alpinelinux.org") || strings.Contains(out, "mirrors.aliyun.com/alpine") {
		t.Fatalf("old mirror hosts remain:\n%s", out)
	}
	if strings.Count(out, "https://mirrors.tuna.tsinghua.edu.cn/alpine/v3.20/") != 2 {
		t.Fatalf("new mirror not applied:\n%s", out)
	}
}

func TestDebianHelpers(t *testing.T) {
	if !debianHasFirmware("12") || !debianHasFirmware("13.1") {
		t.Fatal("Debian 12+ should support non-free-firmware")
	}
	if debianHasFirmware("11") {
		t.Fatal("Debian 11 should not support non-free-firmware")
	}
	if !debianHasFirmware("") {
		t.Fatal("unknown version should default to firmware enabled")
	}
	if hasActiveDeb("# deb http://example.com\n") {
		t.Fatal("commented deb lines should be ignored")
	}
	if !hasActiveDeb("deb http://example.com bookworm main\n") {
		t.Fatal("active deb line not detected")
	}
}
