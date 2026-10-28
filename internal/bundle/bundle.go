// Package bundle builds deterministic, signed offline bundles and moves them
// through OCI registries as artifacts.
package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// SchemaVersion is the bundle manifest schema.
	SchemaVersion = 1
	// ManifestMediaType identifies the bundle manifest file.
	ManifestMediaType = "application/vnd.mkbox.bundle.v1+json"
	// ArtifactType is used in OCI manifests.
	ArtifactType = "application/vnd.mkbox.bundle.v1"
	// LayerMediaType is the OCI layer media type for the bundle archive.
	LayerMediaType = "application/vnd.mkbox.bundle.v1.tar+gzip"
	// ConfigMediaType is the OCI config media type.
	ConfigMediaType = "application/vnd.mkbox.bundle.config.v1+json"
	// SignatureMediaType identifies signature.json content.
	SignatureMediaType = "application/vnd.mkbox.signature.v1+json"

	manifestName  = "manifest.json"
	sbomName      = "sbom.json"
	checksumsName = "checksums.txt"
	signatureName = "signature.json"
	maxMetaSize   = 4 << 20
	maxEntries    = 100000
)

// Platform describes the target platform of the bundled artifacts.
type Platform struct {
	OS      string `json:"os,omitempty"`
	Arch    string `json:"arch,omitempty"`
	Variant string `json:"variant,omitempty"`
}

// FileEntry describes one bundled file.
type FileEntry struct {
	Name      string `json:"name"`
	MediaType string `json:"mediaType"`
	Size      int64  `json:"size"`
	Digest    string `json:"digest"`
	Mode      int64  `json:"mode"`
}

// Manifest is the canonical description of a bundle. Its JSON encoding is
// what gets signed.
type Manifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	Name          string            `json:"name"`
	Version       string            `json:"version"`
	CreatedAt     string            `json:"createdAt"`
	Builder       string            `json:"builder"`
	Platform      Platform          `json:"platform,omitempty"`
	Source        string            `json:"source,omitempty"`
	Commit        string            `json:"commit,omitempty"`
	License       string            `json:"license,omitempty"`
	Files         []FileEntry       `json:"files"`
	SBOMDigest    string            `json:"sbomDigest,omitempty"`
	SBOMSize      int64             `json:"sbomSize,omitempty"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

// Input is one file to include in a bundle.
type Input struct {
	Path      string
	Name      string
	MediaType string
	Mode      int64
}

// BuildOptions configures Build.
type BuildOptions struct {
	OutPath    string
	Name       string
	Version    string
	CreatedAt  time.Time
	Builder    string
	Platform   Platform
	Source     string
	Commit     string
	License    string
	Inputs     []Input
	SigningKey ed25519.PrivateKey
}

// BuildResult reports what was written.
type BuildResult struct {
	Path     string   `json:"path"`
	Size     int64    `json:"size"`
	Digest   string   `json:"digest"`
	Files    int      `json:"files"`
	Signed   bool     `json:"signed"`
	KeyID    string   `json:"keyId,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// Signature is stored next to the manifest and covers its exact bytes.
type Signature struct {
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"keyId"`
	PublicKey string `json:"publicKey"`
	Signature string `json:"signature"`
}

func canonicalJSON(v any) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func digestFile(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), n, nil
}

func blobPath(digest string) string {
	return "blobs/sha256/" + strings.TrimPrefix(digest, "sha256:")
}

func mediaTypeFor(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return "application/gzip"
	case strings.HasSuffix(lower, ".tar"):
		return "application/x-tar"
	case strings.HasSuffix(lower, ".zip"):
		return "application/zip"
	case strings.HasSuffix(lower, ".json"):
		return "application/json"
	case strings.HasSuffix(lower, ".yaml"), strings.HasSuffix(lower, ".yml"):
		return "application/yaml"
	case strings.HasSuffix(lower, ".txt"), strings.HasSuffix(lower, ".md"):
		return "text/plain"
	}
	return "application/octet-stream"
}

func validateName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("文件名为空")
	}
	clean := path.Clean(strings.ReplaceAll(name, "\\", "/"))
	if clean != name || strings.HasPrefix(clean, "/") || clean == "." || strings.HasPrefix(clean, "../") || clean == ".." {
		return fmt.Errorf("非法文件名 %q（必须是干净的相对路径）", name)
	}
	switch clean {
	case manifestName, sbomName, checksumsName, signatureName:
		return fmt.Errorf("文件名 %q 与保留文件冲突", name)
	}
	if strings.HasPrefix(clean, "blobs/") {
		return fmt.Errorf("文件名 %q 与保留目录冲突", name)
	}
	return nil
}

// Build writes a deterministic tar.gz bundle. The same inputs, timestamp and
// signing key always produce byte-identical output.
func Build(opts BuildOptions) (*BuildResult, error) {
	if strings.TrimSpace(opts.OutPath) == "" {
		return nil, fmt.Errorf("必须指定输出路径")
	}
	if opts.Name == "" {
		opts.Name = "bundle"
	}
	if opts.Version == "" {
		opts.Version = "0.0.0"
	}
	if opts.Builder == "" {
		opts.Builder = "mkbox"
	}
	if len(opts.Inputs) == 0 {
		return nil, fmt.Errorf("至少需要一个 --file 输入")
	}
	if opts.CreatedAt.IsZero() {
		opts.CreatedAt = sourceDateEpoch()
	}
	opts.CreatedAt = opts.CreatedAt.UTC().Truncate(time.Second)
	created := opts.CreatedAt.Format(time.RFC3339)

	type prepared struct {
		input  Input
		digest string
		size   int64
	}
	preparedFiles := make([]prepared, 0, len(opts.Inputs))
	seen := map[string]bool{}
	for _, in := range opts.Inputs {
		if in.Name == "" {
			in.Name = filepath.Base(in.Path)
		}
		in.Name = strings.ReplaceAll(in.Name, "\\", "/")
		if err := validateName(in.Name); err != nil {
			return nil, err
		}
		if seen[in.Name] {
			return nil, fmt.Errorf("重复的文件名 %q", in.Name)
		}
		seen[in.Name] = true
		if in.MediaType == "" {
			in.MediaType = mediaTypeFor(in.Name)
		}
		if in.Mode == 0 {
			in.Mode = 0o644
		}
		digest, size, err := digestFile(in.Path)
		if err != nil {
			return nil, fmt.Errorf("读取 %s: %w", in.Path, err)
		}
		preparedFiles = append(preparedFiles, prepared{input: in, digest: digest, size: size})
	}
	sort.Slice(preparedFiles, func(i, j int) bool { return preparedFiles[i].input.Name < preparedFiles[j].input.Name })

	m := Manifest{
		SchemaVersion: SchemaVersion,
		MediaType:     ManifestMediaType,
		Name:          opts.Name,
		Version:       opts.Version,
		CreatedAt:     created,
		Builder:       opts.Builder,
		Platform:      opts.Platform,
		Source:        opts.Source,
		Commit:        opts.Commit,
		License:       opts.License,
		Files:         make([]FileEntry, 0, len(preparedFiles)),
	}
	for _, p := range preparedFiles {
		m.Files = append(m.Files, FileEntry{
			Name:      p.input.Name,
			MediaType: p.input.MediaType,
			Size:      p.size,
			Digest:    p.digest,
			Mode:      p.input.Mode,
		})
	}
	sbomInputs := make([]sbomFile, 0, len(preparedFiles))
	for _, p := range preparedFiles {
		sbomInputs = append(sbomInputs, sbomFile{name: p.input.Name, digest: p.digest})
	}
	sbomBytes, err := buildSBOM(m, sbomInputs)
	if err != nil {
		return nil, err
	}
	m.SBOMDigest = digestBytes(sbomBytes)
	m.SBOMSize = int64(len(sbomBytes))
	manifestBytes, err := canonicalJSON(m)
	if err != nil {
		return nil, err
	}

	var checksums bytes.Buffer
	entries := make([]struct {
		name, digest string
	}, 0, len(preparedFiles)+2)
	entries = append(entries, struct{ name, digest string }{manifestName, digestBytes(manifestBytes)})
	entries = append(entries, struct{ name, digest string }{sbomName, m.SBOMDigest})
	for _, p := range preparedFiles {
		entries = append(entries, struct{ name, digest string }{blobPath(p.digest), p.digest})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	for _, e := range entries {
		fmt.Fprintf(&checksums, "%s  %s\n", strings.TrimPrefix(e.digest, "sha256:"), e.name)
	}

	var signature *Signature
	if len(opts.SigningKey) > 0 {
		sig, err := SignManifest(manifestBytes, opts.SigningKey)
		if err != nil {
			return nil, err
		}
		signature = sig
	}

	dir := filepath.Dir(opts.OutPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建输出目录: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".mkbox-bundle-*")
	if err != nil {
		return nil, fmt.Errorf("创建输出文件: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	gz, err := gzip.NewWriterLevel(tmp, gzip.BestCompression)
	if err != nil {
		tmp.Close()
		return nil, err
	}
	gz.Header.ModTime = opts.CreatedAt
	gz.Header.OS = 255
	gz.Header.Name = ""
	gz.Header.Comment = ""
	tw := tar.NewWriter(gz)

	writeMeta := func(name string, data []byte) error {
		return writeTarBytes(tw, name, data, opts.CreatedAt)
	}
	if err := writeMeta(manifestName, manifestBytes); err != nil {
		return nil, closeBundle(tmp, gz, tw, err)
	}
	for _, p := range preparedFiles {
		if err := writeTarFile(tw, blobPath(p.digest), p.input.Path, p.size, p.digest, opts.CreatedAt); err != nil {
			return nil, closeBundle(tmp, gz, tw, err)
		}
	}
	if err := writeMeta(sbomName, sbomBytes); err != nil {
		return nil, closeBundle(tmp, gz, tw, err)
	}
	if err := writeMeta(checksumsName, checksums.Bytes()); err != nil {
		return nil, closeBundle(tmp, gz, tw, err)
	}
	if signature != nil {
		sigBytes, err := canonicalJSON(signature)
		if err != nil {
			return nil, closeBundle(tmp, gz, tw, err)
		}
		if err := writeMeta(signatureName, sigBytes); err != nil {
			return nil, closeBundle(tmp, gz, tw, err)
		}
	}
	if err := tw.Close(); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("关闭 tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("关闭 gzip: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("关闭输出: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(tmpName, opts.OutPath); err != nil {
		return nil, fmt.Errorf("保存 bundle: %w", err)
	}
	digest, size, err := digestFile(opts.OutPath)
	if err != nil {
		return nil, err
	}
	res := &BuildResult{
		Path:   opts.OutPath,
		Size:   size,
		Digest: digest,
		Files:  len(preparedFiles),
		Signed: signature != nil,
	}
	if signature != nil {
		res.KeyID = signature.KeyID
	} else {
		res.Warnings = append(res.Warnings, "bundle 未签名；生产环境建议使用 --key 签名")
	}
	return res, nil
}

type sbomFile struct {
	name, digest string
}

func sourceDateEpoch() time.Time {
	if raw := strings.TrimSpace(os.Getenv("SOURCE_DATE_EPOCH")); raw != "" {
		var sec int64
		if _, err := fmt.Sscanf(raw, "%d", &sec); err == nil && sec > 0 {
			return time.Unix(sec, 0).UTC()
		}
	}
	return time.Now().UTC()
}

func writeTarBytes(tw *tar.Writer, name string, data []byte, mod time.Time) error {
	hdr := &tar.Header{
		Name:     name,
		Mode:     0o644,
		Size:     int64(len(data)),
		ModTime:  mod,
		Typeflag: tar.TypeReg,
		Format:   tar.FormatUSTAR,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("写入 %s 头: %w", name, err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("写入 %s: %w", name, err)
	}
	return nil
}

func writeTarFile(tw *tar.Writer, name, src string, size int64, digest string, mod time.Time) error {
	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("打开 %s: %w", src, err)
	}
	defer f.Close()
	hdr := &tar.Header{
		Name:     name,
		Mode:     0o644,
		Size:     size,
		ModTime:  mod,
		Typeflag: tar.TypeReg,
		Format:   tar.FormatUSTAR,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("写入 %s 头: %w", name, err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tw, h), f)
	if err != nil {
		return fmt.Errorf("复制 %s: %w", src, err)
	}
	if n != size {
		return fmt.Errorf("文件 %s 在构建过程中发生变化（大小 %d != %d），请重试", src, n, size)
	}
	got := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if got != digest {
		return fmt.Errorf("文件 %s 在构建过程中发生变化（摘要不匹配），请重试", src)
	}
	return nil
}

func closeBundle(tmp *os.File, gz *gzip.Writer, tw *tar.Writer, cause error) error {
	_ = tw.Close()
	_ = gz.Close()
	_ = tmp.Close()
	return cause
}

// SignManifest produces a deterministic Ed25519 signature over manifest bytes.
func SignManifest(manifest []byte, key ed25519.PrivateKey) (*Signature, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("无效的 Ed25519 私钥")
	}
	pub := key.Public().(ed25519.PublicKey)
	sig := ed25519.Sign(key, manifest)
	return &Signature{
		Algorithm: "ed25519",
		KeyID:     KeyID(pub),
		PublicKey: base64.StdEncoding.EncodeToString(pub),
		Signature: base64.StdEncoding.EncodeToString(sig),
	}, nil
}

// KeyID returns a short stable fingerprint of a public key.
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:8])
}

// GenerateKeyPair writes a PKCS#8 private key and PKIX public key. Existing
// files are left untouched unless force is true.
func GenerateKeyPair(dir string, force bool) (privPath, pubPath, keyID string, err error) {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return "", "", "", err
	}
	if strings.TrimSpace(dir) == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", "", err
	}
	privPath = filepath.Join(dir, "mkbox-bundle.key")
	pubPath = filepath.Join(dir, "mkbox-bundle.pub")
	if !force {
		for _, p := range []string{privPath, pubPath} {
			if _, statErr := os.Stat(p); statErr == nil {
				return "", "", "", fmt.Errorf("%s 已存在（使用 --force 覆盖）", p)
			}
		}
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", "", "", err
	}
	pubDER, err := x509.MarshalPKIXPublicKey(priv.Public())
	if err != nil {
		return "", "", "", err
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	if err := os.WriteFile(privPath, privPEM, 0o600); err != nil {
		return "", "", "", err
	}
	if err := os.Chmod(privPath, 0o600); err != nil {
		return "", "", "", err
	}
	if err := os.WriteFile(pubPath, pubPEM, 0o644); err != nil {
		return "", "", "", err
	}
	return privPath, pubPath, KeyID(priv.Public().(ed25519.PublicKey)), nil
}

// LoadPrivateKey reads an Ed25519 private key from PEM (PKCS#8).
func LoadPrivateKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("%s 不是 PEM 文件", path)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("解析私钥 %s: %w", path, err)
	}
	ed, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s 不是 Ed25519 私钥", path)
	}
	return ed, nil
}

// LoadPublicKey reads an Ed25519 public key from PEM, or derives it from a
// private key file.
func LoadPublicKey(path string) (ed25519.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("%s 不是 PEM 文件", path)
	}
	if pub, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		ed, ok := pub.(ed25519.PublicKey)
		if !ok {
			return nil, fmt.Errorf("%s 不是 Ed25519 公钥", path)
		}
		return ed, nil
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if ed, ok := key.(ed25519.PrivateKey); ok {
			return ed.Public().(ed25519.PublicKey), nil
		}
	}
	return nil, fmt.Errorf("无法解析 Ed25519 公钥 %s", path)
}

// VerifyOptions configures VerifyFile.
type VerifyOptions struct {
	// TrustedKey, when set, requires a valid signature from exactly this key.
	TrustedKey ed25519.PublicKey
	// RequireSignature rejects unsigned bundles. Signatures are required by
	// default; set AllowUnsigned to opt out for locally built artifacts.
	RequireSignature bool
	// AllowUnsigned accepts bundles without a signature.
	AllowUnsigned bool
}

// VerifyResult is the outcome of bundle verification.
type VerifyResult struct {
	Valid    bool     `json:"valid"`
	Signed   bool     `json:"signed"`
	Trusted  bool     `json:"trusted"`
	KeyID    string   `json:"keyId,omitempty"`
	Manifest Manifest `json:"manifest"`
	Files    int      `json:"files"`
	Size     int64    `json:"size"`
	Warnings []string `json:"warnings,omitempty"`
	Errors   []string `json:"errors,omitempty"`
}

// VerifyFile checks structure, digests, checksums and signature. It does not
// extract anything to disk.
func VerifyFile(filePath string, opts VerifyOptions) (*VerifyResult, error) {
	res := &VerifyResult{}
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("打开 bundle: %w（需要 .tar.gz）", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	var manifestBytes, sbomBytes, checksumsBytes, sigBytes []byte
	blobs := map[string]struct {
		size   int64
		digest string
	}{}
	seen := map[string]bool{}
	entries := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("读取 bundle: %w", err)
		}
		entries++
		if entries > maxEntries {
			return nil, fmt.Errorf("bundle 条目过多")
		}
		name := path.Clean(strings.ReplaceAll(hdr.Name, "\\", "/"))
		if strings.HasPrefix(name, "/") || name == ".." || strings.HasPrefix(name, "../") {
			return nil, fmt.Errorf("bundle 包含非法路径 %q", hdr.Name)
		}
		if seen[name] {
			return nil, fmt.Errorf("bundle 包含重复条目 %q", name)
		}
		seen[name] = true
		if hdr.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("bundle 包含非普通文件 %q", name)
		}
		switch {
		case name == manifestName, name == sbomName, name == checksumsName, name == signatureName:
			data, err := readLimited(tr, maxMetaSize)
			if err != nil {
				return nil, fmt.Errorf("读取 %s: %w", name, err)
			}
			switch name {
			case manifestName:
				manifestBytes = data
			case sbomName:
				sbomBytes = data
			case checksumsName:
				checksumsBytes = data
			case signatureName:
				sigBytes = data
			}
		case strings.HasPrefix(name, "blobs/sha256/"):
			h := sha256.New()
			n, err := io.Copy(h, tr)
			if err != nil {
				return nil, fmt.Errorf("读取 %s: %w", name, err)
			}
			blobs[name] = struct {
				size   int64
				digest string
			}{size: n, digest: "sha256:" + hex.EncodeToString(h.Sum(nil))}
		default:
			res.Errors = append(res.Errors, "未知条目 "+name)
			if _, err := io.Copy(io.Discard, tr); err != nil {
				return nil, err
			}
		}
	}
	if len(manifestBytes) == 0 {
		res.Errors = append(res.Errors, "缺少 manifest.json")
		return res, fmt.Errorf("bundle 校验失败: %s", strings.Join(res.Errors, "; "))
	}
	if err := json.Unmarshal(manifestBytes, &res.Manifest); err != nil {
		res.Errors = append(res.Errors, "manifest.json 解析失败: "+err.Error())
		return res, fmt.Errorf("bundle 校验失败: %s", strings.Join(res.Errors, "; "))
	}
	m := res.Manifest
	if m.SchemaVersion != SchemaVersion || m.MediaType != ManifestMediaType {
		res.Errors = append(res.Errors, fmt.Sprintf("不支持的 manifest 版本/类型 (%d, %s)", m.SchemaVersion, m.MediaType))
	}
	if len(m.Files) == 0 {
		res.Errors = append(res.Errors, "manifest 未声明任何文件")
	}
	referenced := map[string]bool{}
	for _, fe := range m.Files {
		if err := validateName(fe.Name); err != nil {
			res.Errors = append(res.Errors, err.Error())
			continue
		}
		if !strings.HasPrefix(fe.Digest, "sha256:") || len(fe.Digest) != 71 {
			res.Errors = append(res.Errors, "非法摘要 "+fe.Digest)
			continue
		}
		bp := blobPath(fe.Digest)
		referenced[bp] = true
		got, ok := blobs[bp]
		if !ok {
			res.Errors = append(res.Errors, "缺少 blob "+bp)
			continue
		}
		if got.digest != fe.Digest {
			res.Errors = append(res.Errors, "blob 摘要不匹配 "+bp)
		}
		if got.size != fe.Size {
			res.Errors = append(res.Errors, fmt.Sprintf("blob 大小不匹配 %s (%d != %d)", bp, got.size, fe.Size))
		}
		res.Files++
		res.Size += got.size
	}
	for bp := range blobs {
		if !referenced[bp] {
			res.Errors = append(res.Errors, "存在未声明的 blob "+bp)
		}
	}
	if len(sbomBytes) == 0 {
		res.Errors = append(res.Errors, "缺少 sbom.json")
	} else if m.SBOMDigest == "" {
		res.Errors = append(res.Errors, "manifest 未声明 SBOM 摘要")
	} else {
		if got := digestBytes(sbomBytes); got != m.SBOMDigest {
			res.Errors = append(res.Errors, "SBOM 摘要不匹配")
		}
		if int64(len(sbomBytes)) != m.SBOMSize {
			res.Errors = append(res.Errors, "SBOM 大小不匹配")
		}
	}
	if len(checksumsBytes) == 0 {
		res.Errors = append(res.Errors, "缺少 checksums.txt")
	} else if err := verifyChecksums(string(checksumsBytes), manifestBytes, sbomBytes, blobs); err != nil {
		res.Errors = append(res.Errors, err.Error())
	}
	if len(sigBytes) == 0 {
		res.Warnings = append(res.Warnings, "bundle 未签名")
		if len(opts.TrustedKey) > 0 || opts.RequireSignature || !opts.AllowUnsigned {
			res.Errors = append(res.Errors, "bundle 未签名（使用 --allow-unsigned 接受未签名产物）")
		}
	} else {
		var sig Signature
		if err := json.Unmarshal(sigBytes, &sig); err != nil {
			res.Errors = append(res.Errors, "signature.json 解析失败: "+err.Error())
		} else if sig.Algorithm != "ed25519" {
			res.Errors = append(res.Errors, "不支持的签名算法 "+sig.Algorithm)
		} else {
			pubRaw, err := base64.StdEncoding.DecodeString(sig.PublicKey)
			sigRaw, sigErr := base64.StdEncoding.DecodeString(sig.Signature)
			if err != nil || len(pubRaw) != ed25519.PublicKeySize {
				res.Errors = append(res.Errors, "签名公钥无效")
			} else if sigErr != nil || !ed25519.Verify(ed25519.PublicKey(pubRaw), manifestBytes, sigRaw) {
				res.Errors = append(res.Errors, "签名校验失败（manifest 已被篡改或签名不匹配）")
			} else {
				res.Signed = true
				res.KeyID = sig.KeyID
				if KeyID(ed25519.PublicKey(pubRaw)) != sig.KeyID {
					res.Errors = append(res.Errors, "签名 keyId 与公钥不匹配")
				}
				if len(opts.TrustedKey) > 0 {
					if !bytes.Equal(ed25519.PublicKey(pubRaw), opts.TrustedKey) {
						res.Errors = append(res.Errors, "bundle 签名者与可信公钥不匹配")
					} else {
						res.Trusted = true
					}
				}
			}
		}
	}
	res.Valid = len(res.Errors) == 0
	if !res.Valid {
		return res, fmt.Errorf("bundle 校验失败: %s", strings.Join(res.Errors, "; "))
	}
	return res, nil
}

func readLimited(r io.Reader, max int64) ([]byte, error) {
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if n > max {
		return nil, fmt.Errorf("元数据超过 %d 字节", max)
	}
	return buf.Bytes(), nil
}

func verifyChecksums(text string, manifestBytes, sbomBytes []byte, blobs map[string]struct {
	size   int64
	digest string
}) error {
	want := map[string]string{
		manifestName: digestBytes(manifestBytes),
	}
	if len(sbomBytes) > 0 {
		want[sbomName] = digestBytes(sbomBytes)
	}
	for name, b := range blobs {
		want[name] = b.digest
	}
	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != 64 {
			return fmt.Errorf("checksums.txt 行格式错误: %q", line)
		}
		got[fields[1]] = "sha256:" + fields[0]
	}
	for name, d := range want {
		if got[name] != d {
			return fmt.Errorf("checksums.txt 与 %s 不一致", name)
		}
	}
	if len(got) != len(want) {
		return fmt.Errorf("checksums.txt 条目数量不一致")
	}
	return nil
}

// InspectResult is a lightweight bundle summary.
type InspectResult struct {
	Manifest  Manifest   `json:"manifest"`
	Signature *Signature `json:"signature,omitempty"`
	Entries   int        `json:"entries"`
	Size      int64      `json:"size"`
}

// Inspect reads metadata without verifying every blob.
func Inspect(filePath string) (*InspectResult, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("打开 bundle: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	res := &InspectResult{Size: st.Size()}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		res.Entries++
		switch path.Clean(hdr.Name) {
		case manifestName:
			data, err := readLimited(tr, maxMetaSize)
			if err != nil {
				return nil, err
			}
			if err := json.Unmarshal(data, &res.Manifest); err != nil {
				return nil, err
			}
		case signatureName:
			data, err := readLimited(tr, maxMetaSize)
			if err != nil {
				return nil, err
			}
			var sig Signature
			if err := json.Unmarshal(data, &sig); err == nil {
				res.Signature = &sig
			}
		default:
			if _, err := io.Copy(io.Discard, tr); err != nil {
				return nil, err
			}
		}
	}
	if res.Manifest.MediaType == "" {
		return nil, fmt.Errorf("缺少 manifest.json")
	}
	return res, nil
}

// ArchiveName returns a deterministic default filename for a bundle.
func ArchiveName(name, version string) string {
	if name == "" {
		name = "bundle"
	}
	if version == "" {
		version = "0.0.0"
	}
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '-'
	}, name)
	return safe + "-" + version + ".mkbox.tar.gz"
}

// SBOM (SPDX 2.3 subset) construction.

type sbomCreationInfo struct {
	Created  string   `json:"created"`
	Creators []string `json:"creators"`
}

type sbomChecksum struct {
	Algorithm     string `json:"algorithm"`
	ChecksumValue string `json:"checksumValue"`
}

type sbomPackage struct {
	SPDXID           string         `json:"SPDXID"`
	Name             string         `json:"name"`
	Version          string         `json:"versionInfo"`
	DownloadLocation string         `json:"downloadLocation"`
	FilesAnalyzed    bool           `json:"filesAnalyzed"`
	Checksums        []sbomChecksum `json:"checksums"`
	LicenseConcluded string         `json:"licenseConcluded"`
	LicenseDeclared  string         `json:"licenseDeclared"`
}

type sbomDoc struct {
	SPDXID            string           `json:"SPDXID"`
	SPDXVersion       string           `json:"spdxVersion"`
	CreationInfo      sbomCreationInfo `json:"creationInfo"`
	Name              string           `json:"name"`
	DataLicense       string           `json:"dataLicense"`
	DocumentNamespace string           `json:"documentNamespace"`
	Packages          []sbomPackage    `json:"packages"`
}

func buildSBOM(m Manifest, files []sbomFile) ([]byte, error) {
	aggregate := sha256.New()
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, f.name+":"+f.digest)
	}
	sort.Strings(names)
	for _, n := range names {
		io.WriteString(aggregate, n+"\n")
	}
	doc := sbomDoc{
		SPDXID:      "SPDXRef-DOCUMENT",
		SPDXVersion: "SPDX-2.3",
		CreationInfo: sbomCreationInfo{
			Created:  m.CreatedAt,
			Creators: []string{"Tool: mkbox-bundle"},
		},
		Name:              m.Name + "-" + m.Version,
		DataLicense:       "CC0-1.0",
		DocumentNamespace: "https://mkbox.dev/spdx/" + url.PathEscape(m.Name) + "/" + url.PathEscape(m.Version) + "/" + hex.EncodeToString(aggregate.Sum(nil))[:16],
		Packages:          make([]sbomPackage, 0, len(files)),
	}
	license := m.License
	if license == "" {
		license = "NOASSERTION"
	}
	for i, f := range files {
		doc.Packages = append(doc.Packages, sbomPackage{
			SPDXID:           fmt.Sprintf("SPDXRef-Package-%d", i+1),
			Name:             f.name,
			Version:          m.Version,
			DownloadLocation: "NOASSERTION",
			FilesAnalyzed:    false,
			Checksums:        []sbomChecksum{{Algorithm: "SHA256", ChecksumValue: strings.TrimPrefix(f.digest, "sha256:")}},
			LicenseConcluded: license,
			LicenseDeclared:  license,
		})
	}
	return canonicalJSON(doc)
}
