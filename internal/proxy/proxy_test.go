package proxy

import (
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	valid := []string{
		"http://proxy.example.com:7890",
		"https://user:pass@proxy.example.com:8443",
		"socks5://127.0.0.1:1080",
		"socks5h://127.0.0.1:1080",
	}
	for _, raw := range valid {
		if err := Validate(raw); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", raw, err)
		}
	}
	invalid := []string{
		"",
		"proxy.example.com:7890",
		"ftp://proxy.example.com",
		"http://",
		"http://proxy.example.com/path",
	}
	for _, raw := range invalid {
		if err := Validate(raw); err == nil {
			t.Errorf("Validate(%q) expected error", raw)
		}
	}
}

func TestParseScopes(t *testing.T) {
	got, err := ParseScopes("system, shell,apt")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"system", "shell", "apt"} {
		if !got[k] {
			t.Errorf("scope %s missing: %v", k, got)
		}
	}
	if got["docker"] {
		t.Errorf("docker scope should not be set: %v", got)
	}

	all, err := ParseScopes("all")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"system", "shell", "apt", "docker", "containerd"} {
		if !all[k] {
			t.Errorf("all scope missing %s", k)
		}
	}

	for _, bad := range []string{"", "system,unknown"} {
		if _, err := ParseScopes(bad); err == nil {
			t.Errorf("ParseScopes(%q) expected error", bad)
		}
	}
}

func TestUpdateEnvironment(t *testing.T) {
	existing := `PATH="/usr/local/sbin:/usr/local/bin"
HTTP_PROXY="http://old:8080"
export HTTPS_PROXY="http://old:8080"
http_proxy="http://old:8080"
MY_VAR=1
`
	out := UpdateEnvironment(existing, "http://new:7890", []string{"localhost,127.0.0.1"})
	if strings.Contains(out, "old:8080") {
		t.Fatalf("old proxy not removed:\n%s", out)
	}
	for _, want := range []string{
		`PATH="/usr/local/sbin:/usr/local/bin"`,
		`MY_VAR=1`,
		`HTTP_PROXY="http://new:7890"`,
		`HTTPS_PROXY="http://new:7890"`,
		`NO_PROXY="localhost,127.0.0.1"`,
		`http_proxy="http://new:7890"`,
		`https_proxy="http://new:7890"`,
		`no_proxy="localhost,127.0.0.1"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("environment missing %q:\n%s", want, out)
		}
	}

	cleared := UpdateEnvironment(out, "", nil)
	if strings.Contains(cleared, "PROXY") || strings.Contains(cleared, "proxy") {
		t.Fatalf("proxy keys not cleared:\n%s", cleared)
	}
	if !strings.Contains(cleared, "MY_VAR=1") {
		t.Fatalf("unrelated keys lost:\n%s", cleared)
	}
}

func TestShellEnv(t *testing.T) {
	out := ShellEnv("socks5://127.0.0.1:1080", []string{"localhost", "10.0.0.0/8"})
	for _, want := range []string{
		"export HTTP_PROXY='socks5://127.0.0.1:1080'",
		"export HTTPS_PROXY='socks5://127.0.0.1:1080'",
		"export http_proxy='socks5://127.0.0.1:1080'",
		"export https_proxy='socks5://127.0.0.1:1080'",
		"export NO_PROXY='localhost,10.0.0.0/8'",
		"export no_proxy='localhost,10.0.0.0/8'",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("shell env missing %q:\n%s", want, out)
		}
	}
}

func TestAptConfContent(t *testing.T) {
	out := AptConfContent("http://proxy.example.com:7890")
	if !strings.Contains(out, `Acquire::http::Proxy "http://proxy.example.com:7890";`) {
		t.Fatalf("apt http proxy missing:\n%s", out)
	}
	if !strings.Contains(out, `Acquire::https::Proxy "http://proxy.example.com:7890";`) {
		t.Fatalf("apt https proxy missing:\n%s", out)
	}
}

func TestIsManagedKey(t *testing.T) {
	for _, line := range []string{"HTTP_PROXY=x", "http_proxy=x", "NO_PROXY=x", "all_proxy=x", "export HTTPS_PROXY=x"} {
		if !isManagedKey(line) {
			t.Errorf("isManagedKey(%q) = false", line)
		}
	}
	for _, line := range []string{"PATH=/bin", "# HTTP_PROXY=x", "MY_HTTP_PROXY=x"} {
		if isManagedKey(line) {
			t.Errorf("isManagedKey(%q) = true", line)
		}
	}
}
