package k8s

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/makebox/makebox/internal/execx"
	"github.com/makebox/makebox/internal/logx"
	"github.com/makebox/makebox/internal/system"
)

// JoinOptions controls node joining.
type JoinOptions struct {
	Distro          string
	Server          string
	Token           string
	CACertHash      string
	ControlPlane    bool
	CertificateKey  string
	CRISocket       string
	FromFile        string
	NodeIP          string
	Mirrors         []string
	ImageRepository string
	K3sMirror       bool
	SkipPreflight   bool
	CNI             string
}

// Join adds a node to an existing cluster.
func Join(osInfo *system.OS, r *execx.Runner, fs *system.FS, opts JoinOptions) error {
	if !osInfo.IsRoot && !r.DryRun {
		return fmt.Errorf("此操作需要 root 权限，请使用 sudo 运行")
	}
	if opts.Distro == "" {
		if execx.Has("k3s") {
			opts.Distro = "k3s"
		} else {
			opts.Distro = "kubeadm"
		}
	}
	if opts.Distro == "k3s" {
		return joinK3s(osInfo, r, fs, opts)
	}
	return joinKubeadm(osInfo, r, fs, opts)
}

func joinKubeadm(osInfo *system.OS, r *execx.Runner, fs *system.FS, opts JoinOptions) error {
	if !execx.Has("kubeadm") && !r.DryRun {
		return fmt.Errorf("未找到 kubeadm，请先运行: sudo mkbox k8s install --version <v1.x>")
	}

	// Validate every input before preflight changes the host. A typo in the
	// token or server should never disable swap or rewrite sysctl first.
	server := ""
	joinScript := ""
	if opts.FromFile != "" {
		data, err := os.ReadFile(opts.FromFile)
		if err != nil {
			return fmt.Errorf("读取 %s: %w", opts.FromFile, err)
		}
		first := firstCommandLine(string(data))
		if !strings.HasPrefix(first, "kubeadm join") {
			return fmt.Errorf("%s 不是有效的 kubeadm join 脚本", opts.FromFile)
		}
		joinScript = opts.FromFile
	} else {
		if opts.Server == "" || opts.Token == "" {
			return fmt.Errorf("必须提供 --server 与 --token（或在控制面运行 mkbox k8s join-command 获取完整命令）")
		}
		if opts.CACertHash == "" {
			return fmt.Errorf("必须提供 --ca-cert-hash；在控制面执行 mkbox k8s join-command 可直接获得完整加入命令")
		}
		normalized, err := normalizeJoinServer(opts.Server)
		if err != nil {
			return err
		}
		if err := validateJoinCredentials(opts.Token, opts.CACertHash, opts.CertificateKey, opts.ControlPlane); err != nil {
			return err
		}
		server = normalized
	}

	if !opts.SkipPreflight {
		pf := &Preflight{
			OS:              osInfo,
			R:               r,
			FS:              fs,
			Distro:          "kubeadm",
			CNI:             opts.CNI,
			Mirrors:         opts.Mirrors,
			ImageRepository: opts.ImageRepository,
		}
		if err := pf.Run(); err != nil {
			return err
		}
	} else {
		logx.Warnf("已跳过系统预检")
	}

	if joinScript != "" {
		logx.Header("执行 join 脚本")
		if err := r.Run("bash", joinScript); err != nil {
			return fmt.Errorf("执行 %s 失败: %w", joinScript, err)
		}
		logx.Successf("节点已加入集群")
		return nil
	}

	cri := opts.CRISocket
	if cri == "" {
		cri = DetectCRISocket()
	}
	args := []string{
		"join", server,
		"--token", opts.Token,
		"--discovery-token-ca-cert-hash", opts.CACertHash,
		"--cri-socket", cri,
	}
	if opts.ControlPlane {
		args = append(args, "--control-plane")
		if opts.CertificateKey != "" {
			args = append(args, "--certificate-key", opts.CertificateKey)
		}
	}
	logx.Header("加入 Kubernetes 集群")
	if err := r.Run("kubeadm", args...); err != nil {
		return fmt.Errorf("kubeadm join 失败: %w", err)
	}
	logx.Successf("节点已加入集群，请在控制面执行: kubectl get nodes -o wide")
	return nil
}

func firstCommandLine(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "set ") || strings.HasPrefix(line, "#!/") {
			continue
		}
		return line
	}
	return ""
}

var (
	bootstrapTokenRe = regexp.MustCompile(`^[a-z0-9]{6}\.[a-z0-9]{16}$`)
	caCertHashRe     = regexp.MustCompile(`^sha256:[a-fA-F0-9]{64}$`)
	certificateKeyRe = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)
)

func validateJoinCredentials(token, caHash, certificateKey string, controlPlane bool) error {
	if !bootstrapTokenRe.MatchString(token) {
		return fmt.Errorf("--token 格式无效，应为 6 位小写字母数字、点号、16 位小写字母数字（例如 abcdef.0123456789abcdef）")
	}
	if !caCertHashRe.MatchString(caHash) {
		return fmt.Errorf("--ca-cert-hash 格式无效，应为 sha256: 加 64 位十六进制字符")
	}
	if controlPlane && certificateKey != "" && !certificateKeyRe.MatchString(certificateKey) {
		return fmt.Errorf("--certificate-key 格式无效，应为 64 位十六进制字符")
	}
	return nil
}

func normalizeJoinServer(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("--server 不能为空")
	}
	raw = strings.TrimPrefix(strings.TrimPrefix(raw, "https://"), "http://")
	raw = strings.TrimRight(raw, "/")
	if host, port, err := net.SplitHostPort(raw); err == nil {
		if host == "" || port == "" {
			return "", fmt.Errorf("无效的 --server %q", raw)
		}
		if err := validateServerHostPort(host, port); err != nil {
			return "", fmt.Errorf("无效的 --server %q: %v", raw, err)
		}
		return net.JoinHostPort(host, port), nil
	}
	if ip := net.ParseIP(raw); ip != nil {
		return net.JoinHostPort(raw, "6443"), nil
	}
	if strings.ContainsAny(raw, "/ @") || !hostnameRe.MatchString(raw) {
		return "", fmt.Errorf("无效的 --server %q，请使用 host:port", raw)
	}
	return net.JoinHostPort(raw, "6443"), nil
}

// validateServerHostPort checks a hostname/IP and a numeric port.
func validateServerHostPort(host, port string) error {
	host = strings.Trim(host, "[]")
	if net.ParseIP(host) == nil && !hostnameRe.MatchString(host) {
		return fmt.Errorf("主机名 %q 无效", host)
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("端口 %q 无效", port)
	}
	return nil
}

// validateK3sJoin validates and normalises k3s join inputs.
func validateK3sJoin(server, token string) (string, error) {
	server = strings.TrimSpace(server)
	token = strings.TrimSpace(token)
	if server == "" || token == "" {
		return "", fmt.Errorf("--server 和 --token 不能为空")
	}
	if !strings.HasPrefix(server, "https://") && !strings.HasPrefix(server, "http://") {
		server = "https://" + server
	}
	u, err := url.Parse(server)
	if err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("无效的 k3s server 地址 %q", server)
	}
	if u.User != nil {
		return "", fmt.Errorf("k3s server 地址不能包含用户名或密码")
	}
	host := u.Hostname()
	if port := u.Port(); port != "" {
		if err := validateServerHostPort(host, port); err != nil {
			return "", fmt.Errorf("无效的 k3s server 地址 %q: %v", server, err)
		}
	} else {
		if err := validateServerHostPort(host, "6443"); err != nil {
			return "", fmt.Errorf("无效的 k3s server 地址 %q: %v", server, err)
		}
		u.Host = net.JoinHostPort(host, "6443")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("无效的 k3s server 地址 %q", server)
	}
	return u.String(), nil
}

func joinK3s(osInfo *system.OS, r *execx.Runner, fs *system.FS, opts JoinOptions) error {
	server, err := validateK3sJoin(opts.Server, opts.Token)
	if err != nil {
		return err
	}
	if !opts.SkipPreflight {
		pf := &Preflight{
			OS:      osInfo,
			R:       r,
			FS:      fs,
			Distro:  "k3s",
			CNI:     "flannel",
			Mirrors: opts.Mirrors,
		}
		if err := pf.Run(); err != nil {
			return err
		}
	}
	nodeIP := opts.NodeIP
	if nodeIP == "" {
		if ip, err := system.PrimaryIP(); err == nil {
			nodeIP = ip
		}
	}
	return JoinK3s(r, fs, osInfo, server, opts.Token, nodeIP, opts.Mirrors, opts.K3sMirror)
}
