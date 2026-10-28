package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultValid(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config must be valid: %v", err)
	}
	if len(cfg.Registry.Mirrors) == 0 {
		t.Fatal("default mirrors must not be empty")
	}
}

func TestSaveLoadEmptyMirrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg := Default()
	cfg.Registry.Mirrors = []string{}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("config mode=%o, want 600", st.Mode().Perm())
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Registry.Mirrors == nil {
		t.Fatal("explicitly empty mirrors must stay empty, got nil (defaults restored)")
	}
	if len(loaded.Registry.Mirrors) != 0 {
		t.Errorf("expected empty mirrors, got %v", loaded.Registry.Mirrors)
	}
}

func TestLoadMissingUsesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Registry.Mirrors) == 0 {
		t.Error("missing config should yield default mirrors")
	}
	if cfg.K8s.Distro != "kubeadm" {
		t.Errorf("distro=%q", cfg.K8s.Distro)
	}
}

func TestValidateProxySchemes(t *testing.T) {
	cfg := Default()
	valid := []string{
		"http://127.0.0.1:7890",
		"https://user:pass@proxy.example:8443",
		"socks5://127.0.0.1:1080",
		"socks5h://user:pass@127.0.0.1:1080",
	}
	for _, p := range valid {
		cfg.Proxy.HTTP = p
		cfg.Proxy.HTTPS = p
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate(%q): %v", p, err)
		}
	}
	cfg.Proxy.HTTP = "ftp://127.0.0.1:21"
	if err := cfg.Validate(); err == nil {
		t.Error("ftp proxy should be rejected")
	}
}

func TestValidateURLRejectsNonHTTP(t *testing.T) {
	if err := ValidateURL("socks5://127.0.0.1:1080"); err == nil {
		t.Error("ValidateURL should reject socks5 for registry mirrors")
	}
	if err := ValidateURL("https://registry.example/v2/"); err != nil {
		t.Errorf("valid URL rejected: %v", err)
	}
}

func TestApplyDefaultsKeepsMissingFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := []byte(`{"version":1,"k8s":{"version":"1.32"}}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.K8s.Version != "1.32" {
		t.Errorf("version=%q, want 1.32", cfg.K8s.Version)
	}
	if cfg.Registry.MaxMirrors != 3 {
		t.Errorf("maxMirrors=%d, want 3", cfg.Registry.MaxMirrors)
	}
}

func TestValidateK8sVersionFormats(t *testing.T) {
	cfg := Default()
	for _, v := range []string{"1.35", "v1.35.2", "v1.28.15+k3s1"} {
		cfg.K8s.Version = v
		if err := cfg.Validate(); err != nil {
			t.Errorf("version %q should be accepted: %v", v, err)
		}
	}
	for _, v := range []string{"1.35foo", "2.0", "v1.35.2-rc1", "1"} {
		cfg.K8s.Version = v
		if err := cfg.Validate(); err == nil {
			t.Errorf("version %q should be rejected", v)
		}
	}
}

func TestInvalidConfigRejected(t *testing.T) {
	cases := []string{
		`{"k8s":{"distro":"nomad"}}`,
		`{"k8s":{"version":"2.0"}}`,
		`{"k8s":{"podCIDR":"not-a-cidr"}}`,
		`{"registry":{"mirrors":["ftp://mirror"]}}`,
		`{"mirror":{"os":"nowhere"}}`,
		`{"docker":{"dataRoot":"relative/path"}}`,
		`{"docker":{"dataRoot":"/"}}`,
	}
	for _, raw := range cases {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Errorf("config %s should be rejected", raw)
		}
	}
}

func TestLanguageDefaultsAndValidation(t *testing.T) {
	if got := Default().Language; got != "zh-CN" {
		t.Fatalf("default language = %q, want zh-CN", got)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"language":"en"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Language != "en" {
		t.Fatalf("language = %q, want en", cfg.Language)
	}
	bad := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(bad, []byte(`{"language":"fr"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(bad); err == nil {
		t.Fatal("unsupported language should be rejected")
	}
}

func TestSaveIsAtomicAndReadable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "config.json")
	cfg := Default()
	cfg.K8s.CNI = "calico"
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("saved config is not JSON: %v", err)
	}
	if !strings.Contains(string(data), `"cni": "calico"`) {
		t.Errorf("saved config lacks cni: %s", data)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
}
