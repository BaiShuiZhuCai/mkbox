# 故障排查

遇到问题时，先收集以下信息：

```bash
mkbox version
sudo mkbox doctor
sudo mkbox doctor --net
mkbox config show
```

再结合 `--verbose` 重新执行失败的命令。`--dry-run` 可以先确认 mkbox 计划执行哪些操作，不会修改系统。

## 通用

### 提示需要 root

```bash
sudo mkbox <命令>
```

只读命令（`doctor`、`status`、`show`、`test`、`list`）以及任何带 `--dry-run` 的命令不需要 root。

### 非交互环境无法确认

在脚本、CI 或没有 TTY 的环境里必须显式确认：

```bash
mkbox docker uninstall --purge --yes
```

### 如何回滚文件修改

mkbox 写文件前会生成：

```text
<原文件>.mkbox.bak.<时间戳>
```

可以按时间找到最近一次备份并手动恢复：

```bash
ls -l /etc/docker/daemon.json.mkbox.bak.*
sudo cp /etc/docker/daemon.json.mkbox.bak.20261027-120000 /etc/docker/daemon.json
```

也可以使用 `mkbox backup create` / `mkbox backup restore` 做整组配置快照。

如果是通过 `mkbox apply` 执行的变更，优先使用事务 journal：

```bash
sudo mkbox rollback --dry-run     # 先看会恢复/删除哪些文件
sudo mkbox rollback               # 最近一次事务
sudo mkbox rollback <journal-id>  # 指定事务
```

journal 默认在 `/var/lib/mkbox/journals`，也可通过 `MKBOX_STATE_DIR` 或 `--state-dir` 指定。包含软件包安装、集群初始化等不可逆动作时，`rollback` 需要 `--force`，但它不会自动卸载软件包或重置集群。

### 配置文件损坏

`mkbox config show` 会报告 JSON 解析错误。修复或删除配置文件后重新生成：

```bash
mv ~/.config/makebox/config.json ~/.config/makebox/config.json.bad
mkbox config init
```

## Docker

### Docker 安装失败

检查：

```bash
mkbox docker status
systemctl status docker --no-pager
journalctl -u docker -n 100 --no-pager
```

常见原因：

- **软件源不可用或路径不匹配**：重新选择软件源。

  ```bash
  sudo mkbox docker install --mirror official
  sudo mkbox docker install --mirror aliyun
  ```

- **系统代号识别错误**：检查 `/etc/os-release` 中的 `VERSION_CODENAME` 或 `VERSION_ID`。
- **旧 Docker 包冲突**：Debian/Ubuntu 上可能与 `docker.io`、`podman-docker` 冲突；先卸载冲突包。
- **容器环境没有 systemd**：安装可以完成，但服务不会自动启动。需要手动运行 `dockerd`，或改用带 systemd 的虚拟机。

### `permission denied /var/run/docker.sock`

```bash
sudo usermod -aG docker "$USER"
```

重新登录（或执行 `newgrp docker`）后生效。

### Docker 启动失败

```bash
sudo dockerd --validate --config-file /etc/docker/daemon.json
sudo journalctl -u docker -n 200 --no-pager
```

`daemon.json` 不是合法 JSON 时，mkbox 会拒绝继续写入。修复原文件或恢复 `.mkbox.bak.*`。

### 镜像拉取仍然很慢或失败

```bash
mkbox registry test --all
mkbox docker mirror list
mkbox docker mirror test
```

- 公共镜像可能失效，换用可用地址：

  ```bash
  sudo mkbox docker mirror set https://your-mirror.example.com
  ```

- 如果使用 containerd（kubeadm 节点），确认 `/etc/containerd/certs.d/docker.io/hosts.toml` 存在，并重启 containerd：

  ```bash
  sudo systemctl restart containerd
  sudo systemctl status containerd --no-pager
  ```

- 企业代理环境需要同时配置 Docker/containerd 代理，见下文。

### `docker data-root` 后镜像消失

这是预期行为：mkbox 只修改配置，不迁移数据。停止 Docker 后手动同步：

```bash
sudo systemctl stop docker
sudo rsync -aHAX /var/lib/docker/ /new/path/
sudo systemctl start docker
```

确认无误后再删除旧目录。

## Kubernetes

### preflight 或 init 失败

按顺序检查：

```bash
sudo mkbox k8s preflight --dry-run
sudo mkbox doctor
swapon --show
hostname
sysctl net.ipv4.ip_forward
lsmod | grep -E 'overlay|br_netfilter'
```

常见问题：

- **swap 仍启用**：`sudo swapoff -a`，并检查 `/etc/fstab` 是否已注释 swap 条目。
- **hostname 非法**：必须是小写 DNS 名称。

  ```bash
  sudo hostnamectl set-hostname k8s-node-1
  ```

- **端口被占用**：`ss -lntp | grep -E '6443|10250'`。
- **防火墙拦截**：确认 6443、10250、2379-2380 和 CNI 端口已放行；或先在测试环境使用 `--skip-firewall`。

### kubelet 一直 NotReady

```bash
systemctl status kubelet --no-pager
journalctl -u kubelet -n 200 --no-pager
systemctl status containerd --no-pager
crictl ps -a
crictl images
```

检查：

- containerd 是否设置 `SystemdCgroup = true`：

  ```bash
  grep -n SystemdCgroup /etc/containerd/config.toml
  ```

- CRI socket 是否为 `unix:///run/containerd/containerd.sock`。
- CNI Pod 是否运行：

  ```bash
  kubectl get pods -A -o wide
  kubectl describe node <节点名>
  ```

- 节点时间是否同步（`timedatectl`）。

### 镜像拉取失败（kubeadm）

```bash
sudo kubeadm config images pull --config /etc/makebox/kubeadm-init.yaml
```

如果使用默认 `registry.k8s.io` 受限，换成可达的镜像仓库后重试：

```bash
sudo mkbox k8s init --image-repository registry.aliyuncs.com/google_containers
```

或在安装阶段指定：

```bash
sudo mkbox k8s up --image-repository registry.aliyuncs.com/google_containers
```

已经初始化过但想重新拉取时，先 `mkbox k8s reset`，再重新初始化。

### kubeadm 报 unknown service runtime.v1.RuntimeService

完整报错类似：

```text
failed to create new CRI runtime service: validate CRI v1 runtime API for endpoint
"unix:///run/containerd/containerd.sock": rpc error: code = Unimplemented
desc = unknown service runtime.v1.RuntimeService
```

说明 containerd 在运行，但没有对外提供 CRI 接口，kubeadm 因此无法拉取镜像。最常见原因是发行版软件包默认禁用了 CRI 插件：

```bash
grep -n disabled_plugins /etc/containerd/config.toml
sudo ctr plugins ls | grep -E 'cri|healthcheck'
```

- `/etc/containerd/config.toml` 中出现 `disabled_plugins = ["cri"]`，或 `ctr plugins ls` 里没有 `io.containerd.grpc.v1.cri`（1.x）/ `io.containerd.cri.v1.runtime`（2.x）。
- 处理方式：`sudo mkbox k8s install` 会自动从 `disabled_plugins` 中移除 `cri` 并重启 containerd；手动处理则删除该条目后执行 `sudo systemctl restart containerd`。
- 如果 `ctr plugins ls` 里 CRI 插件状态不是 `ok`，执行 `journalctl -u containerd -n 200 --no-pager` 查看插件加载失败的具体字段。

### CNI 安装失败

CNI 清单默认从 GitHub 下载。网络受限时：

```bash
# 使用 GitHub 代理
sudo mkbox k8s addon install flannel --gh-proxy https://gh-proxy.example.com

# 使用本地清单
sudo mkbox k8s addon apply ./kube-flannel.yml
```

确认 CNI Pod 状态：

```bash
kubectl get pods -A | grep -E 'flannel|calico'
kubectl logs -n kube-flannel -l app=flannel --tail=100
```

Calico 的 Pod 网段必须与 `--pod-cidr` 一致；不一致时重新应用：

```bash
sudo mkbox k8s addon install calico --pod-cidr 10.244.0.0/16
```

### 节点加入失败

- **token 过期**：在控制面重新生成。

  ```bash
  kubeadm token create --print-join-command
  ```

- **CA hash 不匹配**：使用控制面输出的 `sha256:...`，不要手写。
- **server 地址不可达**：从工作节点测试。

  ```bash
  curl -k https://10.0.0.10:6443/version
  ```

- **控制面只有一个节点时**，需要先解除 master 的 NoSchedule 污点才能调度工作负载：

  ```bash
  kubectl taint nodes --all node-role.kubernetes.io/control-plane-
  ```

- **k3s token 路径**：控制面 `/var/lib/rancher/k3s/server/node-token`。

### `kubectl` 访问 127.0.0.1 或旧 IP

重新导出并修正 server：

```bash
mkbox k8s kubeconfig --server https://10.0.0.10:6443 --force
```

如果 kubeconfig 已导出到 `~/.kube/config`，`--force` 会先备份原文件。

### 重置集群

```bash
sudo mkbox k8s reset
sudo mkbox k8s reset --all
```

`--all` 会删除 etcd/kubelet 或 k3s 数据，不可恢复。重置后如仍残留网络设备，可手动清理：

```bash
sudo ip link delete cni0 2>/dev/null || true
sudo ip link delete flannel.1 2>/dev/null || true
```

## 代理

### 代理设置后仍然超时

```bash
mkbox proxy show
mkbox docker proxy show
systemctl show docker --property=Environment --no-pager
grep -i proxy /etc/environment /etc/profile.d/mkbox-proxy.sh
```

注意：

- `/etc/environment` 只对新的登录会话生效；当前 shell 需要重新登录或手动 `export`。
- Docker/containerd 代理需要重启服务。
- 部分工具（旧版 apt、特定语言运行时）对 `socks5h://` 支持不一致；优先使用 `http://` 代理。
- 代理地址中包含特殊字符时需要在 URL 中正确转义。

### 清除代理

```bash
sudo mkbox proxy unset --scope all
```

如果只清了 Docker：

```bash
sudo mkbox docker proxy unset
```

## 镜像与软件源

### `mkbox mirror os` 后 apt 更新失败

mkbox 会写入 Debian/Ubuntu 的 `sources.list` 或 `.sources` 文件，并在失败时保留 `.mkbox.bak.*`。检查：

```bash
grep -R '^deb' /etc/apt/sources.list /etc/apt/sources.list.d/
apt-get update
```

恢复原文件：

```bash
ls -l /etc/apt/sources.list.mkbox.bak.* /etc/apt/sources.list.d/*.mkbox.bak.*
```

RHEL/SUSE 不会自动改写软件源；如需国内镜像，请按发行版文档配置 `dnf`/`zypper` 仓库。

### Arch/Alpine 软件源异常

```bash
# Arch
head -20 /etc/pacman.d/mirrorlist

# Alpine
cat /etc/apk/repositories
```

恢复对应的 `.mkbox.bak.*` 后重新执行 `pacman -Syu` 或 `apk update`。

## 备份与恢复

### 恢复被拒绝

mkbox 会拒绝：

- 绝对路径
- `..` 路径穿越
- 单文件超过 512 MiB

符号链接与特殊文件会被跳过并计数，不会写入目标位置。

确认快照来源可信。若只是需要查看内容：

```bash
tar -tzf snapshot.tar.gz | head
```

### 恢复后服务异常

恢复只覆盖配置，不重启服务。按需重启：

```bash
sudo systemctl daemon-reload
sudo systemctl restart docker containerd kubelet
```

## 收敛与运行时诊断

### `plan` 与预期不一致

`plan` 的期望状态来自当前配置文件。检查：

```bash
mkbox config show
mkbox config path
mkbox plan docker --json | jq '.desired, .observed'
```

如果在脚本中使用了不同的 `MKBOX_CONFIG`，注意命令可能读取另一份配置。

### `diff --exit-code` 返回 2

这不是错误，而是“存在受管文件差异”的约定退出码：

```text
0  已收敛
1  命令失败
2  存在差异
```

在 shell 中 `set -e` 会让脚本提前退出，可显式处理：

```bash
mkbox diff all --exit-code
rc=$?
case "$rc" in
  0) echo converged ;;
  2) echo drift detected ;;
  *) exit "$rc" ;;
esac
```

### `apply` 提示不可逆动作

安装 Docker、执行 `kubeadm init`、修改内核参数等动作无法仅靠恢复文件撤销。确认维护窗口和回滚方案后加 `--force`。`--auto-rollback` 只恢复可回滚的文件，不会卸载软件包。

### `runtime plan` 报告 socket 或 cgroup 冲突

先查看完整事实：

```bash
mkbox runtime detect
mkbox runtime plan
kubectl get nodes -o wide
```

常见根因是 Docker 自带 containerd 与系统 containerd 同时运行、kubelet 仍指向 dockershim、或 Docker 与 kubelet 使用不同 cgroup driver。先停止不用的运行时并统一 socket/cgroup 配置，再重启相关服务；mkbox 不会自动改动这些设置。

## 离线 bundle 与 registry

### bundle 校验失败

```bash
mkbox bundle inspect ./artifact.mkbox.tar.gz --json
mkbox bundle verify ./artifact.mkbox.tar.gz --key ./keys/mkbox-bundle.pub --require-signature
```

常见原因：

- 文件在传输中被截断或修改（gzip/tar 读取错误或摘要不匹配）
- 构建时使用了不同输入、时间戳或签名密钥
- 公钥与签名者不匹配
- bundle 未签名但使用了 `--require-signature`

`bundle` 是确定性归档：同一组输入、`--created-at` 和密钥应生成完全相同的字节；如果两次构建不同，先比较 `--file` 列表和构建时间。

### registry 推送或拉取失败

- 确认 registry 支持 OCI artifact（`artifactType`）而不是只支持容器镜像。
- 内网 HTTP registry 需要 `--plain-http`；公网 registry 不应关闭 TLS 校验。
- 认证失败时检查 `MKBOX_REGISTRY_USER` / `MKBOX_REGISTRY_PASSWORD`，或使用 `--password-stdin`。
- 拉取报摘要或大小不匹配说明 blob 被篡改或 registry 返回了错误内容，应视为供应链风险，不要继续使用该产物。
- 推送前默认校验 bundle；`--skip-verify` 只应在明确知道产物来源时使用。

## 收集诊断信息

提交问题时，建议附上：

```bash
mkbox version
cat /etc/os-release
uname -a
sudo mkbox doctor --net
mkbox config show
sudo systemctl status docker containerd --no-pager
sudo journalctl -u docker -u containerd -u kubelet -n 200 --no-pager
```

请先确认输出中不包含敏感信息；`mkbox config show` 默认会遮盖代理密码，但 `--raw` 不会。
