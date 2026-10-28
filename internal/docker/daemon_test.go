package docker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadDaemonPreservesUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.json")
	raw := `{"debug":true,"log-opts":{"max-size":"10m"},"registry-mirrors":["https://mirror.example.com"]}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := ReadDaemon(path)
	if err != nil {
		t.Fatalf("ReadDaemon: %v", err)
	}
	if v, ok := cfg.Get("debug"); !ok || v != true {
		t.Fatalf("unknown key debug was not preserved: %v %v", v, ok)
	}
	if got := cfg.Mirrors(); len(got) != 1 || got[0] != "https://mirror.example.com" {
		t.Fatalf("unexpected mirrors: %v", got)
	}

	if err := cfg.SetMirrors([]string{"https://a.example.com/", "https://a.example.com", "http://b.example.com"}); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Mirrors(); len(got) != 2 || got[0] != "https://a.example.com" || got[1] != "http://b.example.com" {
		t.Fatalf("mirrors not normalized/deduped: %v", got)
	}

	cfg.SetLogging("json-file", "50m", "3")
	if err := cfg.SetMirrors(nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Get("registry-mirrors"); ok {
		t.Fatal("empty mirrors should remove the key")
	}

	data, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("marshalled config is not valid JSON: %v", err)
	}
	if out["debug"] != true {
		t.Fatal("Marshal lost unknown key")
	}
	logOpts, ok := out["log-opts"].(map[string]any)
	if !ok {
		t.Fatalf("log-opts missing: %v", out["log-opts"])
	}
	if logOpts["max-size"] != "50m" || logOpts["max-file"] != "3" {
		t.Fatalf("unexpected log opts: %v", logOpts)
	}
	if out["log-driver"] != "json-file" {
		t.Fatalf("unexpected log driver: %v", out["log-driver"])
	}
}

func TestReadDaemonMissingAndInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	cfg, err := ReadDaemon(path)
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(cfg.SortedKeys()) != 0 {
		t.Fatalf("expected empty config, got %v", cfg.SortedKeys())
	}

	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte("{not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDaemon(bad); err == nil {
		t.Fatal("invalid JSON should return an error")
	}
}

func TestSetDataRoot(t *testing.T) {
	cfg := NewDaemonConfig()
	cfg.SetDataRoot("/data/docker")
	if v, ok := cfg.Get("data-root"); !ok || v != "/data/docker" {
		t.Fatalf("data-root not set: %v %v", v, ok)
	}
	cfg.SetDataRoot("")
	if _, ok := cfg.Get("data-root"); ok {
		t.Fatal("empty data-root should remove the key")
	}
}

func TestValidatePackageMirror(t *testing.T) {
	for _, mirror := range []string{"", "aliyun", "tuna", "ustc", "huawei", "official"} {
		if err := ValidatePackageMirror(mirror); err != nil {
			t.Errorf("ValidatePackageMirror(%q): %v", mirror, err)
		}
	}
	if err := ValidatePackageMirror("bogus"); err == nil {
		t.Fatal("invalid package mirror should error")
	}
}

func TestValidateDataRoot(t *testing.T) {
	for _, path := range []string{"", "/data/docker", "/var/lib/docker"} {
		if _, err := ValidateDataRoot(path); err != nil {
			t.Errorf("ValidateDataRoot(%q): %v", path, err)
		}
	}
	for _, path := range []string{"relative", "/", ".", "~/docker"} {
		if _, err := ValidateDataRoot(path); err == nil {
			t.Errorf("ValidateDataRoot(%q) should error", path)
		}
	}
}

func TestCleanNoProxy(t *testing.T) {
	got := CleanNoProxy([]string{"localhost, 127.0.0.1", "", "localhost", "10.0.0.0/8"})
	want := []string{"localhost", "127.0.0.1", "10.0.0.0/8"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("CleanNoProxy = %v, want %v", got, want)
	}
}

func TestSystemdProxyEnv(t *testing.T) {
	out := SystemdProxyEnv("http://proxy.example.com:7890", []string{"localhost,10.0.0.0/8"})
	for _, want := range []string{
		"[Service]",
		`Environment="HTTP_PROXY=http://proxy.example.com:7890"`,
		`Environment="HTTPS_PROXY=http://proxy.example.com:7890"`,
		`Environment="http_proxy=http://proxy.example.com:7890"`,
		`Environment="https_proxy=http://proxy.example.com:7890"`,
		`Environment="NO_PROXY=localhost,10.0.0.0/8"`,
		`Environment="no_proxy=localhost,10.0.0.0/8"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("proxy env missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(SystemdProxyEnv("http://proxy:1", nil), "NO_PROXY") {
		t.Fatal("empty no_proxy should not emit NO_PROXY")
	}
}
