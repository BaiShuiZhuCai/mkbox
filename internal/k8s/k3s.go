package k8s

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/makebox/makebox/internal/execx"
	"github.com/makebox/makebox/internal/logx"
	"github.com/makebox/makebox/internal/registry"
	"github.com/makebox/makebox/internal/system"
)

const (
	// K3sRegistriesPath configures k3s registry mirrors.
	K3sRegistriesPath = "/etc/rancher/k3s/registries.yaml"
	// K3sConfigPath is the k3s server/agent configuration.
	K3sConfigPath = "/etc/rancher/k3s/config.yaml"
	// K3sKubeconfig is k3s's admin kubeconfig.
	K3sKubeconfig = "/etc/rancher/k3s/k3s.yaml"
	// K3sTokenPath contains the cluster join token on a server.
	K3sTokenPath = "/var/lib/rancher/k3s/server/node-token"
)

// K3sRegistriesYAML renders k3s registries.yaml for Docker Hub mirrors.
func K3sRegistriesYAML(mirrors []string) string {
	var b strings.Builder
	b.WriteString("# Managed by makebox (mkbox).\n")
	deduped := registry.Dedupe(mirrors)
	if len(deduped) == 0 {
		return b.String()
	}
	b.WriteString("mirrors:\n")
	b.WriteString("  docker.io:\n")
	b.WriteString("    endpoint:\n")
	endpoints := make([]string, 0, len(deduped)+1)
	for _, m := range deduped {
		if m == "https://docker.io" || m == "https://index.docker.io" {
			continue
		}
		endpoints = append(endpoints, m)
	}
	if len(endpoints) == 0 {
		return b.String()
	}
	for _, m := range endpoints {
		fmt.Fprintf(&b, "      - %q\n", m)
	}
	b.WriteString("      - \"https://docker.io\"\n")
	return b.String()
}

func (in *Initializer) initK3s() (*InitResult, error) {
	if !execx.Has("k3s") && !in.R.DryRun {
		return nil, fmt.Errorf("未找到 k3s，请先运行: sudo mkbox k8s install --distro k3s")
	}
	if !in.OS.HasSystemd() && !in.R.DryRun {
		return nil, fmt.Errorf("mkbox 的 k3s 初始化依赖 systemd")
	}
	if _, err := os.Stat(K3sKubeconfig); err == nil {
		return nil, fmt.Errorf("检测到已初始化的 k3s 集群（%s），请先运行: sudo mkbox k8s reset --distro k3s", K3sKubeconfig)
	}
	if in.Opts.CNI != "" && in.Opts.CNI != "flannel" && in.Opts.CNI != "none" {
		logx.Warnf("k3s 默认使用 flannel，忽略 --cni=%s", in.Opts.CNI)
		in.Opts.CNI = "flannel"
	}

	if !in.Opts.SkipPreflight {
		pf := &Preflight{
			OS:                in.OS,
			R:                 in.R,
			FS:                in.FS,
			Distro:            "k3s",
			CNI:               "flannel",
			Mirrors:           in.Opts.Mirrors,
			ImageRepository:   in.Opts.ImageRepository,
			SkipFirewall:      in.Opts.SkipFirewall,
			SELinuxPermissive: in.Opts.SELinuxPermissive,
		}
		if err := pf.Run(); err != nil {
			return nil, err
		}
	}

	nodeIP := strings.TrimSpace(in.Opts.APIAddress)
	if nodeIP == "" {
		ip, err := system.PrimaryIP()
		if err != nil {
			return nil, fmt.Errorf("无法自动检测节点 IP，请使用 --apiserver-address 指定: %w", err)
		}
		nodeIP = ip
	}
	if err := in.FS.Write(K3sRegistriesPath, []byte(K3sRegistriesYAML(in.Opts.Mirrors)), 0o644); err != nil {
		return nil, err
	}

	var b strings.Builder
	b.WriteString("# Managed by makebox (mkbox)\n")
	b.WriteString("write-kubeconfig-mode: \"0644\"\n")
	fmt.Fprintf(&b, "node-ip: %q\n", nodeIP)
	b.WriteString("tls-san:\n")
	fmt.Fprintf(&b, "  - %q\n", nodeIP)
	if hostname, err := os.Hostname(); err == nil {
		fmt.Fprintf(&b, "  - %q\n", hostname)
	}
	if endpoint, err := NormalizeEndpoint(in.Opts.ControlPlaneEndpoint); err == nil && endpoint != "" {
		if host, _, err := net.SplitHostPort(endpoint); err == nil && host != "" {
			fmt.Fprintf(&b, "  - %q\n", host)
		}
	}
	if in.Opts.K3sClusterInit {
		b.WriteString("cluster-init: true\n")
	}
	if err := in.FS.Write(K3sConfigPath, []byte(b.String()), 0o600); err != nil {
		return nil, err
	}
	logx.Successf("已生成 k3s 配置: %s", K3sConfigPath)

	if in.R.DryRun {
		logx.Infof("[dry-run] 将执行: systemctl enable k3s && systemctl restart k3s")
		return &InitResult{NodeIP: nodeIP, Kubeconfig: K3sKubeconfig}, nil
	}

	if err := in.R.Run("systemctl", "enable", "k3s"); err != nil {
		logx.Warnf("设置 k3s 开机启动失败: %v", err)
	}
	if err := in.R.Run("systemctl", "restart", "k3s"); err != nil {
		return nil, fmt.Errorf("启动 k3s 失败，请执行 journalctl -u k3s 查看原因: %w", err)
	}
	if err := waitForFile(K3sKubeconfig, 2*time.Minute); err != nil {
		return nil, fmt.Errorf("等待 k3s 生成 kubeconfig 超时，请执行 journalctl -u k3s 查看原因: %w", err)
	}

	result := &InitResult{NodeIP: nodeIP}
	kubeconfig, err := SetupKubeconfig(in.R, in.FS, in.OS, K3sKubeconfig)
	if err != nil {
		result.Warnings = append(result.Warnings, "写入用户 kubeconfig 失败: "+err.Error())
	} else {
		result.Kubeconfig = kubeconfig
	}
	timeout := in.Opts.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Minute
	}
	if err := WaitForReady(in.R, K3sKubeconfig, timeout); err != nil {
		result.Warnings = append(result.Warnings, err.Error())
	}

	token, err := os.ReadFile(K3sTokenPath)
	if err == nil {
		join := fmt.Sprintf("mkbox k8s join --distro k3s --server https://%s:6443 --token %s", nodeIP, strings.TrimSpace(string(token)))
		result.JoinCommand = join
		script := "#!/usr/bin/env bash\nset -euo pipefail\n" + join + "\n"
		if err := in.FS.Write(filepath.Join(MakeboxDir, "join-k3s.sh"), []byte(script), 0o700); err != nil {
			result.Warnings = append(result.Warnings, "保存 k3s join 脚本失败: "+err.Error())
		}
	} else {
		result.Warnings = append(result.Warnings, "读取 k3s node-token 失败: "+err.Error())
	}
	return result, nil
}

func waitForFile(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if st, err := os.Stat(path); err == nil && st.Size() > 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("文件 %s 未在 %s 内出现", path, timeout)
		}
		time.Sleep(3 * time.Second)
	}
}

// JoinK3s configures and starts a k3s agent.
func JoinK3s(r *execx.Runner, fs *system.FS, osInfo *system.OS, server, token, nodeIP string, mirrors []string, chinaMirror bool) error {
	if !osInfo.IsRoot && !r.DryRun {
		return fmt.Errorf("此操作需要 root 权限，请使用 sudo 运行")
	}
	if !osInfo.HasSystemd() && !r.DryRun {
		return fmt.Errorf("mkbox 的 k3s 加入依赖 systemd")
	}
	server, err := validateK3sJoin(server, token)
	if err != nil {
		return err
	}
	token = strings.TrimSpace(token)

	var b strings.Builder
	b.WriteString("# Managed by makebox (mkbox)\n")
	fmt.Fprintf(&b, "server: %q\n", server)
	fmt.Fprintf(&b, "token: %q\n", token)
	if nodeIP != "" {
		fmt.Fprintf(&b, "node-ip: %q\n", nodeIP)
	}
	if err := fs.Write(K3sConfigPath, []byte(b.String()), 0o600); err != nil {
		return err
	}
	if err := fs.Write(K3sRegistriesPath, []byte(K3sRegistriesYAML(mirrors)), 0o644); err != nil {
		return err
	}
	if err := InstallK3s(r, osInfo, "", chinaMirror, true); err != nil {
		return err
	}
	if r.DryRun {
		logx.Infof("[dry-run] 将执行: systemctl enable k3s-agent && systemctl restart k3s-agent")
		return nil
	}
	if err := r.Run("systemctl", "enable", "k3s-agent"); err != nil {
		logx.Warnf("设置 k3s-agent 开机启动失败: %v", err)
	}
	if err := r.Run("systemctl", "restart", "k3s-agent"); err != nil {
		return fmt.Errorf("启动 k3s-agent 失败，请执行 journalctl -u k3s-agent 查看原因: %w", err)
	}
	logx.Successf("节点已加入 k3s 集群")
	return nil
}

// DetectClusterKubeconfig returns the most likely existing local cluster
// kubeconfig, or an empty string when no cluster is configured.
func DetectClusterKubeconfig() string {
	for _, p := range []string{AdminKubeconfig, K3sKubeconfig} {
		if st, err := os.Stat(p); err == nil && st.Size() > 0 {
			return p
		}
	}
	userKubeconfig := filepath.Join(system.UserHome(), ".kube", "config")
	if st, err := os.Stat(userKubeconfig); err == nil && st.Size() > 0 {
		return userKubeconfig
	}
	return ""
}
