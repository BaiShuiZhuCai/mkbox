package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/makebox/makebox/internal/config"
	"github.com/makebox/makebox/internal/i18n"
)

func TestRedactProxy(t *testing.T) {
	got := redactProxy("http://alice:s3cret@proxy.example.com:7890")
	if strings.Contains(got, "s3cret") {
		t.Fatalf("password was not redacted: %s", got)
	}
	if !strings.Contains(got, "REDACTED") || !strings.Contains(got, "alice") {
		t.Fatalf("unexpected redacted URL: %s", got)
	}
	if got := redactProxy("http://proxy.example.com:7890"); got != "http://proxy.example.com:7890" {
		t.Fatalf("URL without password changed: %s", got)
	}
	if got := redactProxy(""); got != "" {
		t.Fatalf("empty proxy should stay empty, got %q", got)
	}
	if got := redactProxy("https://bob:token@ghproxy.example.com"); strings.Contains(got, "token") {
		t.Fatalf("GitHub proxy password was not redacted: %s", got)
	}
}

func TestScanFlag(t *testing.T) {
	cases := []struct {
		args  []string
		name  string
		value string
		found bool
	}{
		{[]string{"doctor", "--lang", "en"}, "lang", "en", true},
		{[]string{"--lang=en", "doctor"}, "lang", "en", true},
		{[]string{"--lang", "zh-CN", "--lang", "en"}, "lang", "en", true},
		{[]string{"doctor", "--config", "/tmp/c.json"}, "config", "/tmp/c.json", true},
		{[]string{"--", "--lang", "en"}, "lang", "", false},
		{[]string{"doctor"}, "lang", "", false},
	}
	for _, c := range cases {
		got, found := scanFlag(c.args, c.name)
		if got != c.value || found != c.found {
			t.Errorf("scanFlag(%v, %q) = %q, %v; want %q, %v", c.args, c.name, got, found, c.value, c.found)
		}
	}
}

func TestLocalizeCommands(t *testing.T) {
	t.Cleanup(func() { _ = i18n.SetLanguage(i18n.Chinese) })
	if err := i18n.SetLanguage(i18n.English); err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{Use: "demo <快照>", Short: "列出已有快照"}
	sub := &cobra.Command{Use: "sub", Short: "显示版本与构建信息"}
	sub.Flags().Bool("force", false, "覆盖已有配置")
	cmd.AddCommand(sub)
	localizeCommands(cmd)
	if cmd.Short != "List existing snapshots" {
		t.Errorf("short = %q", cmd.Short)
	}
	if sub.Short != "Show version and build information" {
		t.Errorf("sub short = %q", sub.Short)
	}
	if got := sub.Flags().Lookup("force").Usage; got != "Overwrite the existing configuration" {
		t.Errorf("flag usage = %q", got)
	}
}

func TestConfigGetSet(t *testing.T) {
	cfg := config.Default()
	if err := configSet(cfg, "registry.mirrors", "https://a.example.com, https://b.example.com"); err != nil {
		t.Fatal(err)
	}
	got, err := configGet(cfg, "registry.mirrors")
	if err != nil || got != "https://a.example.com,https://b.example.com" {
		t.Fatalf("registry.mirrors = %q, %v", got, err)
	}

	if err := configSet(cfg, "proxy.noproxy", "localhost, 127.0.0.1,10.0.0.0/8"); err != nil {
		t.Fatal(err)
	}
	got, _ = configGet(cfg, "proxy.noProxy")
	if got != "localhost,127.0.0.1,10.0.0.0/8" {
		t.Fatalf("proxy.noProxy = %q", got)
	}

	if err := configSet(cfg, "k8s.cni", "calico"); err != nil {
		t.Fatal(err)
	}
	if cfg.K8s.CNI != "calico" {
		t.Fatalf("k8s.cni = %q", cfg.K8s.CNI)
	}

	if err := configSet(cfg, "network.checkTimeoutMs", "not-a-number"); err == nil {
		t.Fatal("invalid integer should error")
	}
	if _, err := configGet(cfg, "unknown.key"); err == nil {
		t.Fatal("unknown key should error")
	}
	if err := configSet(cfg, "registry.mirrors", ""); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Registry.Mirrors) != 0 {
		t.Fatalf("empty value should clear mirrors, got %v", cfg.Registry.Mirrors)
	}
}

func TestAppliedMirrors(t *testing.T) {
	a := &App{Cfg: &config.Config{
		Registry: config.RegistryConf{
			Mirrors:    []string{"https://a.example.com", "https://b.example.com", "https://c.example.com"},
			MaxMirrors: 2,
		},
	}}
	got, err := a.appliedMirrors(nil, false)
	if err != nil || len(got) != 2 || got[0] != "https://a.example.com" {
		t.Fatalf("appliedMirrors = %v, %v", got, err)
	}
	if got, err := a.appliedMirrors(nil, true); err != nil || got != nil {
		t.Fatalf("disabled mirrors should be nil, got %v, %v", got, err)
	}
	if got, err := a.appliedMirrors([]string{"https://x.example.com"}, false); err != nil || len(got) != 1 || got[0] != "https://x.example.com" {
		t.Fatalf("explicit mirrors = %v, %v", got, err)
	}
	if _, err := a.appliedMirrors([]string{"://bad"}, false); err == nil {
		t.Fatal("invalid explicit mirror should error")
	}
}

func TestValidateCNI(t *testing.T) {
	for _, cni := range []string{"flannel", "calico", "none"} {
		if err := validateCNI(cni); err != nil {
			t.Errorf("validateCNI(%q): %v", cni, err)
		}
	}
	if err := validateCNI("bogus"); err == nil {
		t.Fatal("invalid CNI should error")
	}
}

func TestFormatSize(t *testing.T) {
	tests := map[int64]string{
		0:                  "0 B",
		1023:               "1023 B",
		1024:               "1.0 KiB",
		1024 * 1024:        "1.0 MiB",
		1536 * 1024:        "1.5 MiB",
		1024 * 1024 * 1024: "1.0 GiB",
	}
	for in, want := range tests {
		if got := formatSize(in); got != want {
			t.Errorf("formatSize(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestPick(t *testing.T) {
	if got := pick(" value ", "fallback"); got != "value" {
		t.Fatalf("pick returned %q", got)
	}
	if got := pick("  ", "fallback"); got != "fallback" {
		t.Fatalf("pick returned %q", got)
	}
}
