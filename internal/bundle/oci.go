package bundle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Reference is a parsed OCI reference (registry/repository:tag).
type Reference struct {
	Registry   string
	Repository string
	Tag        string
}

func (r Reference) String() string {
	return r.Registry + "/" + r.Repository + ":" + r.Tag
}

// ParseReference parses registry/repository:tag. If no registry is present,
// docker.io is assumed. A missing tag defaults to "latest".
func ParseReference(raw string) (Reference, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "https://")
	raw = strings.TrimPrefix(raw, "http://")
	raw = strings.TrimSuffix(raw, "/")
	if raw == "" {
		return Reference{}, fmt.Errorf("镜像引用为空")
	}
	if strings.ContainsAny(raw, " \t\r\n?#") {
		return Reference{}, fmt.Errorf("非法镜像引用 %q", raw)
	}
	ref := Reference{Tag: "latest"}
	rest := raw
	if i := strings.LastIndex(raw, ":"); i > strings.LastIndex(raw, "/") {
		ref.Tag = raw[i+1:]
		rest = raw[:i]
	}
	if ref.Tag == "" {
		return Reference{}, fmt.Errorf("镜像 tag 为空")
	}
	first := rest
	if i := strings.Index(rest, "/"); i >= 0 {
		first = rest[:i]
	}
	if strings.ContainsAny(first, ".:") || first == "localhost" {
		ref.Registry = first
		if i := strings.Index(rest, "/"); i >= 0 {
			ref.Repository = rest[i+1:]
		}
	} else {
		ref.Registry = "docker.io"
		ref.Repository = rest
		if !strings.Contains(ref.Repository, "/") {
			ref.Repository = "library/" + ref.Repository
		}
	}
	if ref.Repository == "" {
		return Reference{}, fmt.Errorf("镜像引用缺少仓库名: %q", raw)
	}
	for _, seg := range strings.Split(ref.Repository, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return Reference{}, fmt.Errorf("非法仓库名 %q", ref.Repository)
		}
	}
	if strings.Contains(ref.Registry, "/") {
		return Reference{}, fmt.Errorf("非法 registry %q", ref.Registry)
	}
	return ref, nil
}

func (r Reference) apiHost() string {
	if r.Registry == "docker.io" {
		return "registry-1.docker.io"
	}
	return r.Registry
}

func (r Reference) repoPath() string {
	parts := strings.Split(r.Repository, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

func isLoopbackHost(host string) bool {
	h := host
	if hostname, _, err := net.SplitHostPort(host); err == nil {
		h = hostname
	}
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// Client talks to an OCI distribution registry.
type Client struct {
	HTTP      *http.Client
	Username  string
	Password  string
	PlainHTTP bool
	UserAgent string
	tokens    map[string]string
}

// NewClient returns a client with conservative timeouts.
func NewClient() *Client {
	return &Client{
		HTTP: &http.Client{
			Timeout: 10 * time.Minute,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("重定向次数过多")
				}
				// Never forward basic auth to a different host.
				if len(via) > 0 && req.URL.Host != via[0].URL.Host {
					req.Header.Del("Authorization")
				}
				return nil
			},
		},
		UserAgent: "mkbox-bundle/1",
		tokens:    map[string]string{},
	}
}

func (c *Client) baseURL(ref Reference) string {
	scheme := "https"
	if c.PlainHTTP || isLoopbackHost(ref.Registry) {
		scheme = "http"
	}
	return scheme + "://" + ref.apiHost()
}

func (c *Client) newRequest(method, rawURL string, body io.Reader, size int64, contentType string) (*http.Request, error) {
	req, err := http.NewRequest(method, rawURL, body)
	if err != nil {
		return nil, err
	}
	if size >= 0 {
		req.ContentLength = size
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if c.Username != "" {
		req.SetBasicAuth(c.Username, c.Password)
	}
	return req, nil
}

type bearerChallenge struct {
	Realm, Service, Scope string
}

var authParamRe = regexp.MustCompile(`([a-zA-Z]+)="([^"]*)"`)

func parseBearer(header string) (bearerChallenge, bool) {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(header)), "bearer ") {
		return bearerChallenge{}, false
	}
	ch := bearerChallenge{}
	for _, m := range authParamRe.FindAllStringSubmatch(header, -1) {
		switch strings.ToLower(m[1]) {
		case "realm":
			ch.Realm = m[2]
		case "service":
			ch.Service = m[2]
		case "scope":
			ch.Scope = m[2]
		}
	}
	return ch, ch.Realm != ""
}

// do sends a request and transparently handles Bearer token challenges.
func (c *Client) do(req *http.Request, scope string) (*http.Response, error) {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}
	challenge, ok := parseBearer(resp.Header.Get("WWW-Authenticate"))
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	if !ok {
		if c.Username == "" || req.Header.Get("Authorization") != "" && !strings.HasPrefix(req.Header.Get("Authorization"), "Basic ") {
			return nil, fmt.Errorf("%s %s: 未授权（401），请检查用户名/密码", req.Method, req.URL.Redacted())
		}
		clone := req.Clone(context.Background())
		clone.SetBasicAuth(c.Username, c.Password)
		if req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			clone.Body = body
		}
		return c.HTTP.Do(clone)
	}
	if challenge.Scope == "" {
		challenge.Scope = scope
	}
	token, err := c.token(challenge)
	if err != nil {
		return nil, err
	}
	clone := req.Clone(context.Background())
	clone.Header.Set("Authorization", "Bearer "+token)
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		clone.Body = body
	}
	return c.HTTP.Do(clone)
}

func (c *Client) token(ch bearerChallenge) (string, error) {
	key := ch.Realm + "|" + ch.Service + "|" + ch.Scope
	if tok, ok := c.tokens[key]; ok {
		return tok, nil
	}
	u, err := url.Parse(ch.Realm)
	if err != nil {
		return "", fmt.Errorf("解析令牌地址: %w", err)
	}
	q := u.Query()
	if ch.Service != "" {
		q.Set("service", ch.Service)
	}
	if ch.Scope != "" {
		q.Set("scope", ch.Scope)
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		return "", err
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if c.Username != "" {
		req.SetBasicAuth(c.Username, c.Password)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("获取 registry token: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("获取 registry token 失败: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var payload struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", fmt.Errorf("解析 registry token: %w", err)
	}
	tok := payload.Token
	if tok == "" {
		tok = payload.AccessToken
	}
	if tok == "" {
		return "", fmt.Errorf("registry token 响应缺少 token")
	}
	c.tokens[key] = tok
	return tok, nil
}

func statusError(op string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	msg := strings.TrimSpace(string(body))
	if len(msg) > 300 {
		msg = msg[:300]
	}
	if msg == "" {
		return fmt.Errorf("%s: HTTP %d", op, resp.StatusCode)
	}
	return fmt.Errorf("%s: HTTP %d: %s", op, resp.StatusCode, msg)
}

type ociDescriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type ociManifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	ArtifactType  string            `json:"artifactType,omitempty"`
	Config        ociDescriptor     `json:"config"`
	Layers        []ociDescriptor   `json:"layers"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

// ArtifactMetadata becomes OCI manifest annotations.
type ArtifactMetadata struct {
	Name         string
	Version      string
	BundleDigest string
	Signed       bool
	KeyID        string
}

// PushResult describes a completed push.
type PushResult struct {
	Reference      string `json:"reference"`
	ManifestDigest string `json:"manifestDigest"`
	BundleDigest   string `json:"bundleDigest"`
	Size           int64  `json:"size"`
}

func (c *Client) ensureBlob(ref Reference, digest string, size int64, open func() (io.ReadCloser, error)) error {
	base := c.baseURL(ref)
	blobURL := fmt.Sprintf("%s/v2/%s/blobs/%s", base, ref.repoPath(), digest)
	req, err := c.newRequest("HEAD", blobURL, nil, -1, "")
	if err != nil {
		return err
	}
	resp, err := c.do(req, "repository:"+ref.Repository+":pull,push")
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	if resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("检查 blob %s: HTTP %d", digest, resp.StatusCode)
	}

	uploadURL := fmt.Sprintf("%s/v2/%s/blobs/uploads/", base, ref.repoPath())
	req, err = c.newRequest("POST", uploadURL, nil, 0, "")
	if err != nil {
		return err
	}
	resp, err = c.do(req, "repository:"+ref.Repository+":pull,push")
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusCreated {
		defer resp.Body.Close()
		return statusError("创建 blob 上传", resp)
	}
	loc := resp.Header.Get("Location")
	resp.Body.Close()
	if loc == "" {
		return fmt.Errorf("registry 未返回上传 Location")
	}
	locURL, err := url.Parse(loc)
	if err != nil {
		return err
	}
	reqURL, _ := url.Parse(uploadURL)
	locURL = reqURL.ResolveReference(locURL)
	q := locURL.Query()
	q.Set("digest", digest)
	locURL.RawQuery = q.Encode()

	body, err := open()
	if err != nil {
		return err
	}
	defer body.Close()
	req, err = c.newRequest("PUT", locURL.String(), body, size, "application/octet-stream")
	if err != nil {
		return err
	}
	req.GetBody = func() (io.ReadCloser, error) { return open() }
	resp, err = c.do(req, "repository:"+ref.Repository+":pull,push")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return statusError("上传 blob", resp)
	}
	return nil
}

// PushFile uploads a bundle archive as an OCI artifact.
func (c *Client) PushFile(ref Reference, filePath string, meta ArtifactMetadata) (*PushResult, error) {
	digest, size, err := digestFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("读取 bundle: %w", err)
	}
	if meta.Name == "" {
		meta.Name = filepath.Base(filePath)
	}
	configBytes := []byte("{}")
	configDigest := digestBytes(configBytes)
	configSize := int64(len(configBytes))
	openConfig := func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(configBytes)), nil
	}
	if err := c.ensureBlob(ref, configDigest, configSize, openConfig); err != nil {
		return nil, err
	}
	openBundle := func() (io.ReadCloser, error) { return os.Open(filePath) }
	if err := c.ensureBlob(ref, digest, size, openBundle); err != nil {
		return nil, err
	}
	annotations := map[string]string{
		"org.opencontainers.image.title":   meta.Name,
		"org.opencontainers.image.version": meta.Version,
		"dev.mkbox.bundle.digest":          digest,
	}
	if meta.Signed {
		annotations["dev.mkbox.bundle.signed"] = "true"
	}
	if meta.KeyID != "" {
		annotations["dev.mkbox.bundle.key-id"] = meta.KeyID
	}
	manifest := ociManifest{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.manifest.v1+json",
		ArtifactType:  ArtifactType,
		Config:        ociDescriptor{MediaType: ConfigMediaType, Digest: configDigest, Size: configSize},
		Layers: []ociDescriptor{{
			MediaType:   LayerMediaType,
			Digest:      digest,
			Size:        size,
			Annotations: annotations,
		}},
		Annotations: annotations,
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	manifestDigest := digestBytes(manifestBytes)
	manifestURL := fmt.Sprintf("%s/v2/%s/manifests/%s", c.baseURL(ref), ref.repoPath(), url.PathEscape(ref.Tag))
	req, err := c.newRequest("PUT", manifestURL, bytes.NewReader(manifestBytes), int64(len(manifestBytes)), manifest.MediaType)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req, "repository:"+ref.Repository+":pull,push")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return nil, statusError("推送 manifest", resp)
	}
	if got := resp.Header.Get("Docker-Content-Digest"); got != "" {
		manifestDigest = got
	}
	return &PushResult{
		Reference:      ref.String(),
		ManifestDigest: manifestDigest,
		BundleDigest:   digest,
		Size:           size,
	}, nil
}

// PullResult describes a downloaded artifact.
type PullResult struct {
	Reference      string `json:"reference"`
	Path           string `json:"path"`
	ManifestDigest string `json:"manifestDigest"`
	BundleDigest   string `json:"bundleDigest"`
	Size           int64  `json:"size"`
	Signed         bool   `json:"signed"`
}

// PullFile downloads a bundle artifact and verifies its digest while writing.
func (c *Client) PullFile(ref Reference, outPath string) (*PullResult, error) {
	manifestURL := fmt.Sprintf("%s/v2/%s/manifests/%s", c.baseURL(ref), ref.repoPath(), url.PathEscape(ref.Tag))
	req, err := c.newRequest("GET", manifestURL, nil, -1, "")
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ", "))
	resp, err := c.do(req, "repository:"+ref.Repository+":pull")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError("获取 manifest", resp)
	}
	manifestBytes, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	manifestDigest := digestBytes(manifestBytes)
	if got := resp.Header.Get("Docker-Content-Digest"); got != "" {
		manifestDigest = got
	}
	var manifest ociManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return nil, fmt.Errorf("解析 OCI manifest: %w", err)
	}
	var layer *ociDescriptor
	for i := range manifest.Layers {
		if manifest.Layers[i].MediaType == LayerMediaType {
			layer = &manifest.Layers[i]
			break
		}
	}
	if layer == nil {
		return nil, fmt.Errorf("manifest 中没有 mkbox bundle layer（artifactType %s）", manifest.ArtifactType)
	}
	blobURL := fmt.Sprintf("%s/v2/%s/blobs/%s", c.baseURL(ref), ref.repoPath(), layer.Digest)
	req, err = c.newRequest("GET", blobURL, nil, -1, "")
	if err != nil {
		return nil, err
	}
	resp, err = c.do(req, "repository:"+ref.Repository+":pull")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError("下载 blob", resp)
	}
	if outPath == "" {
		outPath = "bundle.tar.gz"
	}
	dir := filepath.Dir(outPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(dir, ".mkbox-pull-*")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), resp.Body)
	if err != nil {
		tmp.Close()
		return nil, fmt.Errorf("写入 blob: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if layer.Size >= 0 && n != layer.Size {
		return nil, fmt.Errorf("blob 大小不匹配（%d != %d）", n, layer.Size)
	}
	if got := "sha256:" + hex.EncodeToString(h.Sum(nil)); got != layer.Digest {
		return nil, fmt.Errorf("blob 摘要不匹配（%s != %s）", got, layer.Digest)
	}
	if want := layer.Annotations["dev.mkbox.bundle.digest"]; want != "" && want != layer.Digest {
		return nil, fmt.Errorf("manifest 注解摘要不匹配")
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(tmpName, outPath); err != nil {
		return nil, err
	}
	return &PullResult{
		Reference:      ref.String(),
		Path:           outPath,
		ManifestDigest: manifestDigest,
		BundleDigest:   layer.Digest,
		Size:           n,
		Signed:         layer.Annotations["dev.mkbox.bundle.signed"] == "true",
	}, nil
}
