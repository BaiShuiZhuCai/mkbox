package k8s

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/makebox/makebox/internal/execx"
	"github.com/makebox/makebox/internal/logx"
	"github.com/makebox/makebox/internal/system"
)

// Reset removes a local Kubernetes installation.
func Reset(osInfo *system.OS, r *execx.Runner, fs *system.FS, distro string, all bool) error {
	if !osInfo.IsRoot && !r.DryRun {
		return fmt.Errorf("此操作需要 root 权限，请使用 sudo 运行")
	}
	if distro == "" {
		if execx.Has("k3s") {
			distro = "k3s"
		} else {
			distro = "kubeadm"
		}
	}
	logx.Header("重置 Kubernetes（%s）", distro)
	if distro == "k3s" {
		return resetK3s(r, fs, all)
	}
	return resetKubeadm(r, fs, all)
}

func resetKubeadm(r *execx.Runner, fs *system.FS, all bool) error {
	if execx.Has("kubeadm") {
		if err := r.Run("kubeadm", "reset", "-f"); err != nil {
			logx.Warnf("kubeadm reset 执行失败，将继续清理本地文件: %v", err)
		}
	} else {
		logx.Warnf("未找到 kubeadm，仅清理本地文件")
	}
	if execx.Has("ipvsadm") {
		_ = r.Run("ipvsadm", "--clear")
	}
	_ = r.Run("rm", "-rf", "/etc/cni/net.d")
	_ = r.Run("rm", "-rf", "/var/lib/cni")
	_ = fs.Remove(filepath.Join(system.UserHome(), ".kube", "config"))
	_ = fs.Remove(KubeadmConfigPath)
	_ = fs.Remove(JoinScriptPath)
	if all {
		logx.Warnf("正在删除集群数据: /etc/kubernetes /var/lib/etcd /var/lib/kubelet")
		_ = r.Run("rm", "-rf", "/etc/kubernetes", "/var/lib/etcd", "/var/lib/kubelet", "/var/lib/dockershim")
	}
	logx.Successf("kubeadm 集群已重置")
	return nil
}

func resetK3s(r *execx.Runner, fs *system.FS, all bool) error {
	// The official k3s uninstall script always deletes /var/lib/rancher/k3s.
	// Stop and disable the service explicitly so the default reset keeps data.
	service := ""
	for _, candidate := range []string{"k3s", "k3s-agent"} {
		if _, err := os.Stat("/etc/systemd/system/" + candidate + ".service"); err == nil {
			service = candidate
			break
		}
	}
	if service != "" {
		logx.Infof("停止并禁用 %s 服务（默认保留集群数据）", service)
		if err := r.Run("systemctl", "disable", "--now", service); err != nil {
			logx.Warnf("停止 %s 失败，将继续清理本地配置: %v", service, err)
		}
		_ = fs.Remove("/etc/systemd/system/" + service + ".service")
		_ = fs.Remove("/etc/systemd/system/" + service + ".service.env")
		_ = r.Run("systemctl", "daemon-reload")
	} else {
		logx.Warnf("未找到 k3s systemd 服务，可能未安装或为手动安装")
	}
	_ = r.Run("rm", "-rf", "/etc/cni/net.d")
	_ = fs.Remove(filepath.Join(system.UserHome(), ".kube", "config"))
	_ = fs.Remove("/etc/rancher/k3s")
	_ = fs.Remove(filepath.Join(MakeboxDir, "join-k3s.sh"))
	if all {
		logx.Warnf("正在删除 k3s 集群数据: /var/lib/rancher/k3s /var/lib/kubelet /var/lib/cni")
		_ = r.Run("rm", "-rf", "/var/lib/rancher/k3s", "/var/lib/kubelet", "/var/lib/cni", "/var/log/pods", "/var/log/containers")
	} else {
		logx.Infof("已保留 /var/lib/rancher/k3s；如需彻底删除请使用 --all")
	}
	logx.Successf("k3s 集群已重置")
	return nil
}
