package k8s

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/makebox/makebox/internal/execx"
	"github.com/makebox/makebox/internal/system"
)

var serverLineRe = regexp.MustCompile(`(?m)^(\s*server:\s*).*$`)

// ExportKubeconfig copies a cluster kubeconfig to output, optionally rewriting
// the API server address.
func ExportKubeconfig(fs *system.FS, source, output, server string, force bool) (string, error) {
	if source == "" {
		source = DetectClusterKubeconfig()
		if source == "" {
			return "", fmt.Errorf("未找到可用的集群 kubeconfig；请在控制面节点执行，或使用 --source 指定文件")
		}
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return "", fmt.Errorf("读取 %s: %w（请确认已在控制面节点执行）", source, err)
	}
	if server != "" {
		normalized, err := normalizeServerURL(server)
		if err != nil {
			return "", err
		}
		data = serverLineRe.ReplaceAll(data, []byte("${1}"+normalized))
	}
	if output == "-" {
		_, _ = os.Stdout.Write(data)
		return "-", nil
	}
	if output == "" {
		output = filepath.Join(system.UserHome(), ".kube", "config")
	}
	if strings.HasPrefix(output, "~/") {
		output = filepath.Join(system.UserHome(), output[2:])
	}
	output = filepath.Clean(output)
	if source == output && server == "" {
		if !fs.DryRun {
			_ = os.Chmod(output, 0o600)
			_ = system.ChownToSudoUser(output)
		}
		return output, nil
	}
	if _, err := os.Stat(output); err == nil && !force && !fs.DryRun {
		return "", fmt.Errorf("%s 已存在，使用 --force 覆盖（原文件会自动备份）", output)
	}
	if err := fs.EnsureDir(filepath.Dir(output), 0o700); err != nil {
		return "", err
	}
	if err := fs.Write(output, data, 0o600); err != nil {
		return "", err
	}
	if !fs.DryRun {
		_ = os.Chmod(output, 0o600)
		if strings.HasPrefix(output, system.UserHome()) {
			_ = system.ChownToSudoUser(output)
		}
	}
	return output, nil
}

func normalizeServerURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("无效的 server 地址 %q", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("server 地址 %q 必须使用 http 或 https", raw)
	}
	if u.User != nil {
		return "", fmt.Errorf("server 地址 %q 不能包含用户名或密码", raw)
	}
	host := u.Hostname()
	if net.ParseIP(host) == nil && !hostnameRe.MatchString(host) {
		return "", fmt.Errorf("无效的 server 主机名 %q", host)
	}
	if port := u.Port(); port != "" {
		p, err := strconv.Atoi(port)
		if err != nil || p < 1 || p > 65535 {
			return "", fmt.Errorf("无效的 server 端口 %q", port)
		}
	} else {
		u.Host = net.JoinHostPort(host, "6443")
	}
	return u.String(), nil
}

// JoinCommand returns a ready-to-run command for adding a node.
func JoinCommand(osInfo *system.OS, r *execx.Runner, distro string) (string, error) {
	if distro == "" {
		if execx.Has("k3s") {
			distro = "k3s"
		} else {
			distro = "kubeadm"
		}
	}
	if distro == "k3s" {
		if r.DryRun {
			return "mkbox k8s join --distro k3s --server https://<server>:6443 --token <token>", nil
		}
		token, err := os.ReadFile(K3sTokenPath)
		if err != nil {
			return "", fmt.Errorf("读取 k3s token 失败（请在 server 节点执行）: %w", err)
		}
		ip, err := system.PrimaryIP()
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("mkbox k8s join --distro k3s --server https://%s:6443 --token %s",
			ip, strings.TrimSpace(string(token))), nil
	}
	if r.DryRun {
		return "kubeadm join <control-plane>:6443 --token <token> --discovery-token-ca-cert-hash sha256:<hash>", nil
	}
	out, err := r.Output("kubeadm", "token", "create", "--print-join-command")
	if err != nil {
		return "", fmt.Errorf("生成 join 命令失败（请在控制面以 root 执行）: %w", err)
	}
	join := strings.TrimSpace(out)
	if !strings.Contains(join, "--cri-socket") {
		join += " --cri-socket " + DetectCRISocket()
	}
	return join, nil
}

// SaveJoinScript persists a join command to path.
func SaveJoinScript(fs *system.FS, path, command string) error {
	script := "#!/usr/bin/env bash\nset -euo pipefail\n" + command + "\n"
	return fs.Write(path, []byte(script), 0o700)
}
