package backup

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateAndRestore(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "daemon.json"), []byte(`{"log-driver":"json-file"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(src, "conf.d")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "extra.conf"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(src, "daemon.json"), filepath.Join(src, "link.json")); err != nil {
		t.Logf("symlink not supported, continuing: %v", err)
	}

	dir := filepath.Join(t.TempDir(), "backups")
	archive := filepath.Join(dir, "snapshot.tar.gz")
	res, err := Create(Options{Paths: []string{src}, Output: archive})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if res.Files != 2 {
		t.Fatalf("expected 2 files, got %d", res.Files)
	}
	if res.Skipped != 1 {
		t.Fatalf("expected 1 skipped symlink, got %d", res.Skipped)
	}
	if st, err := os.Stat(archive); err != nil || st.Size() == 0 {
		t.Fatalf("archive missing or empty: %v", err)
	}

	entries := readArchive(t, archive)
	for _, want := range []string{"daemon.json", "conf.d/extra.conf"} {
		if !strings.Contains(strings.Join(entries, "\n"), want) {
			t.Fatalf("archive misses %s: %v", want, entries)
		}
	}

	dest := t.TempDir()
	rr, err := Restore(RestoreOptions{Archive: archive, Dest: dest})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if rr.Files != 2 {
		t.Fatalf("expected to restore 2 files, got %d", rr.Files)
	}
	base := filepath.Join(dest, strings.TrimPrefix(filepath.ToSlash(src), "/"))
	data, err := os.ReadFile(filepath.Join(base, "conf.d", "extra.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("unexpected restored content %q", data)
	}
}

func TestCreateDryRun(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.conf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "dry.tar.gz")
	res, err := Create(Options{Paths: []string{src}, Output: archive, DryRun: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if res.Files != 1 {
		t.Fatalf("expected 1 file, got %d", res.Files)
	}
	if _, err := os.Stat(archive); !os.IsNotExist(err) {
		t.Fatalf("dry-run should not create archive, stat err=%v", err)
	}
}

func TestCreateRejectsEmptyExplicitPaths(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "empty.tar.gz")
	if _, err := Create(Options{Paths: []string{"relative/path"}, Output: archive}); err == nil {
		t.Fatal("relative explicit path should be rejected")
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := Create(Options{Paths: []string{missing}, Output: archive}); err == nil {
		t.Fatal("missing explicit path should be rejected")
	}
	if _, err := os.Stat(archive); !os.IsNotExist(err) {
		t.Fatalf("no archive should be created, stat err=%v", err)
	}
}

func TestList(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.tar.gz", "b.tar.gz", "ignore.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	items, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 archives, got %d", len(items))
	}
	items, err = List(filepath.Join(dir, "missing"))
	if err != nil || len(items) != 0 {
		t.Fatalf("missing dir should yield empty list, got %v, %v", items, err)
	}
}

func TestRestoreRejectsTraversal(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "evil.tar.gz")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	payload := []byte("evil")
	if err := tw.WriteHeader(&tar.Header{Name: "../evil.txt", Mode: 0o644, Size: int64(len(payload)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	f.Close()

	dest := t.TempDir()
	if _, err := Restore(RestoreOptions{Archive: archive, Dest: dest}); err == nil {
		t.Fatal("expected traversal archive to be rejected")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dest), "evil.txt")); err == nil {
		t.Fatal("traversal file was written outside destination")
	}
}

func TestSafeRel(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"etc/docker/daemon.json", "etc/docker/daemon.json", false},
		{"./etc/hosts", "etc/hosts", false},
		{"/etc/passwd", "", true},
		{"../etc/passwd", "", true},
		{"a/../../b", "", true},
		{"", "", true},
		{".", "", true},
	}
	for _, tc := range tests {
		got, err := safeRel(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("safeRel(%q) expected error, got %q", tc.in, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("safeRel(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestArchiveNameAndWithinRoot(t *testing.T) {
	if got := archiveName("/etc/docker/daemon.json"); got != "etc/docker/daemon.json" {
		t.Fatalf("archiveName = %q", got)
	}
	if got := archiveName("/"); got != "" {
		t.Fatalf("archiveName(/) = %q", got)
	}
	if !withinRoot("/tmp/root", "/tmp/root/etc/hosts") {
		t.Fatal("expected path inside root")
	}
	if withinRoot("/tmp/root", "/tmp/root-other/etc/hosts") {
		t.Fatal("sibling prefix path must not be treated as inside root")
	}
	if withinRoot("/tmp/root", "/tmp/root/../escape") {
		t.Fatal("traversal path must not be inside root")
	}
}

func readArchive(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var names []string
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		names = append(names, hdr.Name)
	}
	return names
}
