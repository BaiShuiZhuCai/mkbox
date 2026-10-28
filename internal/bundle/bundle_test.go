package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func buildFixture(t *testing.T, out string, key []byte) *BuildResult {
	t.Helper()
	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.json")
	if err := os.WriteFile(a, []byte("hello bundle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte(`{"k":"v"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Build(BuildOptions{
		OutPath:   out,
		Name:      "mkbox-test",
		Version:   "1.2.3",
		CreatedAt: time.Unix(1700000000, 0).UTC(),
		Platform:  Platform{OS: "linux", Arch: "amd64"},
		License:   "MIT",
		Inputs: []Input{
			{Path: a},
			{Path: b},
		},
		SigningKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func testKey(t *testing.T) (privPath, pubPath string) {
	t.Helper()
	dir := t.TempDir()
	priv, pub, _, err := GenerateKeyPair(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	return priv, pub
}

func TestBuildVerifyReproducible(t *testing.T) {
	privPath, pubPath := testKey(t)
	priv, err := LoadPrivateKey(privPath)
	if err != nil {
		t.Fatal(err)
	}
	out1 := filepath.Join(t.TempDir(), "one.tar.gz")
	out2 := filepath.Join(t.TempDir(), "two.tar.gz")
	r1 := buildFixture(t, out1, priv)
	r2 := buildFixture(t, out2, priv)
	if !r1.Signed || r1.KeyID == "" {
		t.Fatalf("expected signed result: %+v", r1)
	}
	if r1.Digest == "" || r2.Digest == "" {
		t.Fatalf("digests missing: %+v %+v", r1, r2)
	}
	d1, _ := os.ReadFile(out1)
	d2, _ := os.ReadFile(out2)
	if !bytes.Equal(d1, d2) {
		t.Fatalf("builds are not byte-identical (%d vs %d bytes)", len(d1), len(d2))
	}
	pub, err := LoadPublicKey(pubPath)
	if err != nil {
		t.Fatal(err)
	}
	vr, err := VerifyFile(out1, VerifyOptions{TrustedKey: pub, RequireSignature: true})
	if err != nil {
		t.Fatal(err)
	}
	if !vr.Valid || !vr.Signed || !vr.Trusted {
		t.Fatalf("verify = %+v", vr)
	}
	if len(vr.Manifest.Files) != 2 || vr.Files != 2 {
		t.Fatalf("files = %+v", vr.Manifest.Files)
	}
	if vr.Manifest.License != "MIT" || vr.Manifest.Platform.Arch != "amd64" {
		t.Fatalf("manifest = %+v", vr.Manifest)
	}
	ins, err := Inspect(out1)
	if err != nil {
		t.Fatal(err)
	}
	if ins.Manifest.Name != "mkbox-test" || ins.Signature == nil {
		t.Fatalf("inspect = %+v", ins)
	}
}

func TestVerifyUnsignedAndRequireSignature(t *testing.T) {
	out := filepath.Join(t.TempDir(), "unsigned.tar.gz")
	buildFixture(t, out, nil)

	// Unsigned bundles are rejected by default; --allow-unsigned is the
	// explicit opt-in for local build artifacts.
	if _, err := VerifyFile(out, VerifyOptions{}); err == nil {
		t.Fatal("unsigned bundle must be rejected by default")
	}
	vr, err := VerifyFile(out, VerifyOptions{AllowUnsigned: true})
	if err != nil {
		t.Fatal(err)
	}
	if vr.Signed || len(vr.Warnings) == 0 {
		t.Fatalf("expected unsigned warning: %+v", vr)
	}
	if _, err := VerifyFile(out, VerifyOptions{RequireSignature: true}); err == nil {
		t.Fatal("expected signature requirement failure")
	}
	_, pubPath := testKey(t)
	pub, err := LoadPublicKey(pubPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyFile(out, VerifyOptions{TrustedKey: pub, AllowUnsigned: true}); err == nil {
		t.Fatal("trusted key must reject an unsigned bundle")
	}
}

func TestVerifyDetectsTamperedManifest(t *testing.T) {
	privPath, _ := testKey(t)
	priv, _ := LoadPrivateKey(privPath)
	src := filepath.Join(t.TempDir(), "signed.tar.gz")
	buildFixture(t, src, priv)
	dst := filepath.Join(t.TempDir(), "tampered.tar.gz")
	rewriteBundle(t, src, dst, func(name string, data []byte) []byte {
		if name != manifestName {
			return data
		}
		var m Manifest
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatal(err)
		}
		m.Name = "evil"
		out, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		return out
	})
	vr, err := VerifyFile(dst, VerifyOptions{})
	if err == nil {
		t.Fatalf("tampered bundle verified: %+v", vr)
	}
	if vr.Valid {
		t.Fatal("result marked valid")
	}
	if !strings.Contains(strings.Join(vr.Errors, ";"), "签名") && !strings.Contains(strings.Join(vr.Errors, ";"), "不一致") {
		t.Fatalf("unexpected errors: %v", vr.Errors)
	}
}

func TestVerifyRejectsPathTraversal(t *testing.T) {
	src := filepath.Join(t.TempDir(), "b.tar.gz")
	buildFixture(t, src, nil)
	dst := filepath.Join(t.TempDir(), "evil.tar.gz")
	rewriteBundle(t, src, dst, nil, "../evil.txt")
	if _, err := VerifyFile(dst, VerifyOptions{}); err == nil || !strings.Contains(err.Error(), "非法路径") {
		t.Fatalf("expected traversal rejection, got %v", err)
	}
}

func TestBuildRejectsUnsafeNames(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	_ = os.WriteFile(a, []byte("x"), 0o644)
	_, err := Build(BuildOptions{
		OutPath: filepath.Join(dir, "out.tar.gz"),
		Inputs:  []Input{{Path: a, Name: "../evil"}},
	})
	if err == nil {
		t.Fatal("expected unsafe name rejection")
	}
	_, err = Build(BuildOptions{
		OutPath: filepath.Join(dir, "out.tar.gz"),
		Inputs:  []Input{{Path: a, Name: manifestName}},
	})
	if err == nil {
		t.Fatal("expected reserved name rejection")
	}
}

func TestKeyPairPermissionsAndLoading(t *testing.T) {
	priv, pub := testKey(t)
	st, err := os.Stat(priv)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode = %o", st.Mode().Perm())
	}
	pubKey, err := LoadPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	derived, err := LoadPublicKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pubKey, derived) {
		t.Fatal("derived public key differs")
	}
	dir := filepath.Dir(priv)
	if _, _, _, err := GenerateKeyPair(dir, false); err == nil {
		t.Fatal("expected existing key refusal")
	}
	if _, _, _, err := GenerateKeyPair(dir, true); err != nil {
		t.Fatal(err)
	}
}

func TestParseReference(t *testing.T) {
	cases := []struct {
		raw      string
		registry string
		repo     string
		tag      string
	}{
		{"ghcr.io/acme/mkbox:v1", "ghcr.io", "acme/mkbox", "v1"},
		{"localhost:5000/test/repo", "localhost:5000", "test/repo", "latest"},
		{"nginx", "docker.io", "library/nginx", "latest"},
		{"acme/app:1.0", "docker.io", "acme/app", "1.0"},
		{"https://registry.example.com/a/b:t", "registry.example.com", "a/b", "t"},
	}
	for _, c := range cases {
		ref, err := ParseReference(c.raw)
		if err != nil {
			t.Fatalf("%s: %v", c.raw, err)
		}
		if ref.Registry != c.registry || ref.Repository != c.repo || ref.Tag != c.tag {
			t.Fatalf("%s parsed as %+v", c.raw, ref)
		}
	}
	for _, bad := range []string{"", "foo:tag:bad", "http:///x", "ghcr.io//x:tag"} {
		if _, err := ParseReference(bad); err == nil {
			t.Fatalf("%q should be rejected", bad)
		}
	}
}

func rewriteBundle(t *testing.T, src, dst string, mutate func(string, []byte) []byte, extraName ...string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	gz, err := gzip.NewReader(in)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	wg := gzip.NewWriter(out)
	tw := tar.NewWriter(wg)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if mutate != nil {
			data = mutate(hdr.Name, data)
		}
		h := *hdr
		h.Size = int64(len(data))
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range extraName {
		data := []byte("evil")
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := wg.Close(); err != nil {
		t.Fatal(err)
	}
}

type fakeRegistry struct {
	mu        sync.Mutex
	server    *httptest.Server
	token     string
	blobs     map[string][]byte
	manifests map[string][]byte
	failBlob  bool
}

func newFakeRegistry(t *testing.T) *fakeRegistry {
	f := &fakeRegistry{
		token:     "test-token",
		blobs:     map[string][]byte{},
		manifests: map[string][]byte{},
	}
	f.server = httptest.NewServer(f)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/token" {
		_ = json.NewEncoder(w).Encode(map[string]string{"token": f.token})
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/v2/") {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="test"`, f.server.URL))
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == "/v2/" || r.URL.Path == "/v2":
		w.WriteHeader(http.StatusOK)
	case strings.HasSuffix(r.URL.Path, "/blobs/uploads/") && r.Method == http.MethodPost:
		w.Header().Set("Location", "/v2/test/repo/blobs/uploads/session")
		w.WriteHeader(http.StatusAccepted)
	case strings.HasSuffix(r.URL.Path, "/blobs/uploads/session") && r.Method == http.MethodPut:
		digest := r.URL.Query().Get("digest")
		data, _ := io.ReadAll(r.Body)
		sum := sha256.Sum256(data)
		if "sha256:"+hex.EncodeToString(sum[:]) != digest && !f.failBlob {
			http.Error(w, "digest mismatch", http.StatusBadRequest)
			return
		}
		f.blobs[digest] = data
		w.WriteHeader(http.StatusCreated)
	case strings.Contains(r.URL.Path, "/blobs/") && r.Method == http.MethodHead:
		digest := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if _, ok := f.blobs[digest]; ok {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	case strings.Contains(r.URL.Path, "/blobs/") && r.Method == http.MethodGet:
		digest := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		data, ok := f.blobs[digest]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if f.failBlob {
			data = []byte("corrupted")
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		w.Write(data)
	case strings.Contains(r.URL.Path, "/manifests/"):
		if r.Method == http.MethodPut {
			data, _ := io.ReadAll(r.Body)
			sum := sha256.Sum256(data)
			digest := "sha256:" + hex.EncodeToString(sum[:])
			f.manifests[digest] = data
			tag := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			f.manifests[tag] = data
			w.Header().Set("Docker-Content-Digest", digest)
			w.WriteHeader(http.StatusCreated)
			return
		}
		tag := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		data, ok := f.manifests[tag]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		sum := sha256.Sum256(data)
		w.Header().Set("Docker-Content-Digest", "sha256:"+hex.EncodeToString(sum[:]))
		w.Write(data)
	default:
		http.NotFound(w, r)
	}
}

func TestOCIPushPullWithBearerAuth(t *testing.T) {
	reg := newFakeRegistry(t)
	privPath, _ := testKey(t)
	priv, _ := LoadPrivateKey(privPath)
	bundlePath := filepath.Join(t.TempDir(), "b.tar.gz")
	buildFixture(t, bundlePath, priv)
	before, _ := os.ReadFile(bundlePath)

	ref, err := ParseReference(reg.server.URL + "/test/repo:v1")
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient()
	client.Username = "user"
	client.Password = "pass"
	meta := ArtifactMetadata{Name: "mkbox-test", Version: "1.2.3", Signed: true, KeyID: "key"}
	push, err := client.PushFile(ref, bundlePath, meta)
	if err != nil {
		t.Fatal(err)
	}
	if push.BundleDigest == "" || push.ManifestDigest == "" {
		t.Fatalf("push = %+v", push)
	}
	out := filepath.Join(t.TempDir(), "pulled.tar.gz")
	pull, err := NewClient().PullFile(ref, out)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(out)
	if !bytes.Equal(before, after) {
		t.Fatal("pulled bundle differs")
	}
	if pull.BundleDigest != push.BundleDigest || !pull.Signed {
		t.Fatalf("pull = %+v", pull)
	}
	if _, err := VerifyFile(out, VerifyOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestOCIPullRejectsCorruptedBlob(t *testing.T) {
	reg := newFakeRegistry(t)
	bundlePath := filepath.Join(t.TempDir(), "b.tar.gz")
	buildFixture(t, bundlePath, nil)
	ref, _ := ParseReference(reg.server.URL + "/test/repo:v1")
	client := NewClient()
	if _, err := client.PushFile(ref, bundlePath, ArtifactMetadata{Name: "x", Version: "1"}); err != nil {
		t.Fatal(err)
	}
	reg.mu.Lock()
	reg.failBlob = true
	reg.mu.Unlock()
	out := filepath.Join(t.TempDir(), "bad.tar.gz")
	if _, err := NewClient().PullFile(ref, out); err == nil {
		t.Fatal("expected digest mismatch")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("corrupted file must not be left behind")
	}
}
