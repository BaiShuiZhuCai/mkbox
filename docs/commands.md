# mkbox 命令参考

本文档对应 `mkbox 0.2.x`。所有命令均支持：

| 全局参数 | 说明 |
| --- | --- |
| `--dry-run` | 只打印将要执行的修改，不写文件、不重启服务 |
| `-y, --yes` | 跳过交互确认（非 TTY 环境必须使用） |
| `--verbose` | 输出调试信息与实际执行的命令 |
| `--no-color` | 禁用彩色输出 |
| `--config <路径>` | 指定配置文件，默认 `~/.config/makebox/config.json` |
| `--lang zh-CN\|en` | 界面语言；默认依次取 `MKBOX_LANG`、配置文件 `language`、`zh-CN` |
| `-h, --help` | 查看命令帮助 |

修改系统状态的大多数命令需要 root；只读命令和 `--dry-run` 不需要。

## 界面语言（i18n）

内置简体中文与英文两套消息目录，默认中文。命令帮助、日志、体检结果与常用提示会随语言切换；尚未翻译的动态错误文本会回退为中文。

```bash
mkbox --lang en doctor          # 单次使用英文
MKBOX_LANG=en mkbox --help      # 通过环境变量
mkbox config set language en    # 持久化到配置文件
```

优先级：`--lang` > `MKBOX_LANG` > 配置文件 `language` > `zh-CN`。不支持的语言会直接报错，不会静默回退。

## 根命令

```bash
mkbox
```

显示系统、Docker、Kubernetes 和 mkbox 配置摘要。

```bash
mkbox --version
mkbox version
```

显示版本、提交、构建时间、Go 运行时、架构与配置文件路径。

## 环境检查

### `mkbox doctor`

```bash
mkbox doctor [--net] [--timeout 5s]
```

只读检查：

- 发行版、包管理器、systemd/openrc
- 内核版本、架构、内存、`/var` 剩余空间
- swap、`overlay` / `br_netfilter` 模块、`ip_forward`、`bridge-nf-call-iptables`
- hostname 合法性、时间同步、防火墙、SELinux
- Docker 安装/运行状态、cgroup driver、镜像加速
- containerd 状态与 `SystemdCgroup`
- kubeadm/kubelet/kubectl、节点状态、6443/10250 端口占用
- 可选的网络与镜像探测（`--net`）

存在严重失败项时返回非零退出码。

### `mkbox net test`

```bash
mkbox net test [--timeout 5s]
```

测试 Docker Hub、Kubernetes 官方源、GitHub、Rancher、镜像站等端点，输出状态码与延迟。

## 镜像加速

### `mkbox registry list`

列出配置中的镜像加速器，并标记实际会写入 Docker/containerd 的数量。

### `mkbox registry test`

```bash
mkbox registry test [镜像地址...] [--all] [--timeout 5s]
```

不带地址时测试配置中的镜像。判断标准是 `GET <mirror>/v2/` 返回 `200` 或 `401`（需要认证也说明服务可达），并按延迟排序。

## 配置

### `mkbox config path|show|init`

```bash
mkbox config path
mkbox config show [--raw]
mkbox config init [--force]
```

`show` 默认遮盖代理 URL 中的密码；`--raw` 显示原文。`init` 在文件已存在时需加 `--force`，覆盖前会备份。

### `mkbox config get|set`

```bash
mkbox config get <键>
mkbox config set <键> <值>
```

示例：

```bash
mkbox config get k8s.cni
mkbox config set k8s.cni calico
mkbox config set registry.mirrors 'https://a.example.com,https://b.example.com'
mkbox config set proxy.noproxy 'localhost,127.0.0.1,10.0.0.0/8'
mkbox config set registry.mirrors ''   # 清空内置候选，之后需显式传入镜像地址
```

可用键：

```text
version
language
registry.mirrors
registry.maxMirrors
registry.timeoutSeconds
docker.installMirror
docker.logDriver
docker.logMaxSize
docker.logMaxFile
docker.dataRoot
k8s.distro
k8s.version
k8s.imageRepository
k8s.repoMirror
k8s.podCIDR
k8s.serviceCIDR
k8s.cni
proxy.http
proxy.https
proxy.noProxy
mirror.os
network.ghProxy
network.checkTimeoutMs
```

## 备份与恢复

### `mkbox backup create`

```bash
mkbox backup create [--dir <目录>] [-o <文件>] [--path <路径> ...]
```

- 默认目录：root 为 `/var/backups/makebox`，普通用户为 `~/.makebox/backups`。
- `--path` 可重复；一旦指定，就不再使用默认路径列表。
- 默认快照不包含镜像层、容器数据、etcd 数据。

### `mkbox backup list`

```bash
mkbox backup list [--dir <目录>]
```

### `mkbox backup restore`

```bash
mkbox backup restore <快照> [--dest /] [--force]
```

- 拒绝绝对路径与 `..` 路径穿越。
- 跳过符号链接与特殊文件。
- 单文件上限 512 MiB。
- 覆盖前逐文件备份。
- `--dest` 可指向临时目录做演练。

## Docker

### `mkbox docker install`

```bash
mkbox docker install \
  [--mirror aliyun|tuna|ustc|huawei|official] \
  [--user <用户>] \
  [--probe] \
  [--registry-mirror <地址> ...] \
  [--no-registry-mirror]
```

行为：

1. 根据发行版写入 Docker 官方或国内镜像软件源。
2. 安装 `docker-ce`、CLI、containerd、Buildx、Compose 插件（Arch/Alpine 使用发行版包）。
3. 启动并设置开机自启。
4. 写入 `daemon.json` 的镜像加速、日志轮转（默认 100m × 3）。
5. 将 `--user` 或 sudo 调用者加入 `docker` 组。

`--mirror` 只影响 Docker 软件包源，不影响容器镜像加速。Arch/Alpine 使用系统自身软件源。

### `mkbox docker status`

显示版本、运行状态、数据目录、镜像加速数量。

### `mkbox docker uninstall`

```bash
mkbox docker uninstall [--purge]
```

`--purge` 会额外删除 `/var/lib/docker` 与 Docker 配置，不可恢复。共享的 `/var/lib/containerd` 不会被删除，以免影响同时使用的 Kubernetes。

### `mkbox docker mirror set|unset|list|show|test`

```bash
sudo mkbox docker mirror set [镜像地址...] [--probe]
sudo mkbox docker mirror unset
mkbox docker mirror list
mkbox docker mirror show
mkbox docker mirror test [--timeout 5s]
```

- `set` 不带地址时重新应用配置中的列表。
- 地址自动补全 `https://`、去重、去尾斜杠。
- 设置后重启 Docker 并校验 `docker info`。
- `unset` 从 `daemon.json` 删除 `registry-mirrors` 并重启。

### `mkbox docker proxy set|unset|show`

```bash
sudo mkbox docker proxy set <代理地址> [--no-proxy localhost,10.0.0.0/8]
sudo mkbox docker proxy unset
mkbox docker proxy show
```

通过 `/etc/systemd/system/docker.service.d/http-proxy.conf` 同时设置大小写代理变量，然后重启 Docker。

### `mkbox docker data-root`

```bash
sudo mkbox docker data-root /data/docker
sudo mkbox docker data-root ""     # 删除 data-root 配置
```

只修改配置，不迁移已有数据。切换后原 `/var/lib/docker` 中的镜像不会自动出现在新目录。

## 系统代理

```bash
sudo mkbox proxy set <代理地址> [--scope system,shell,apt,docker,containerd,all] [--no-proxy ...]
sudo mkbox proxy unset [--scope ...]
mkbox proxy show
```

默认 scope 为 `system,shell,apt,docker,containerd`。Docker/containerd 未安装时自动跳过。支持协议：

```text
http://  https://  socks5://  socks5h://
```

写入位置：

| scope | 文件 |
| --- | --- |
| `system` | `/etc/environment` |
| `shell` | `/etc/profile.d/mkbox-proxy.sh` |
| `apt` | `/etc/apt/apt.conf.d/95mkbox-proxy` |
| `docker` | `/etc/systemd/system/docker.service.d/http-proxy.conf` |
| `containerd` | `/etc/systemd/system/containerd.service.d/http-proxy.conf` |

## 软件源与开发镜像

### `mkbox mirror os`

```bash
sudo mkbox mirror os <aliyun|tuna|ustc|huawei>
```

- Ubuntu/Debian：自动识别 `sources.list` 或 deb822（`.sources`），写入后执行 `apt-get update`。
- Arch：在 `/etc/pacman.d/mirrorlist` 顶部插入镜像。
- Alpine：替换 `/etc/apk/repositories` 中的镜像主机并执行 `apk update`。
- RHEL/SUSE：返回提示，不自动改写。

### `mkbox mirror dev`

```bash
sudo mkbox mirror dev [--pip] [--npm] [--go] [--all]
```

不带选项时配置全部：

- pip：`/etc/pip.conf` 使用阿里云 PyPI。
- npm：`npm config set registry https://registry.npmmirror.com --global`。
- Go：`go env -w GOPROXY=https://goproxy.cn,direct`，并写入 `/etc/profile.d/mkbox-go.sh`。

### `mkbox mirror list|status`

```bash
mkbox mirror list
mkbox mirror status
```

## Kubernetes

### `mkbox k8s preflight`

```bash
sudo mkbox k8s preflight \
  [--cni flannel|calico] \
  [--distro kubeadm|k3s] \
  [--skip-firewall] \
  [--selinux-permissive] \
  [--registry-mirror ...] \
  [--image-repository ...] \
  [--no-registry-mirror]
```

执行：

- 校验内核与 hostname。
- `swapoff -a` 并注释 `/etc/fstab` 中的 swap 条目。
- 加载 `overlay`、`br_netfilter`（按需）并写入 `/etc/modules-load.d/mkbox-k8s.conf`。
- 写入 `/etc/sysctl.d/99-mkbox-k8s.conf`：`ip_forward`、`bridge-nf-call-*`。
- 配置 containerd `SystemdCgroup = true`、pause 镜像和 certs.d。
- 放行 firewalld 或 UFW 端口；`--skip-firewall` 跳过。
- 可选将 SELinux 切为 permissive。

### `mkbox k8s install`

```bash
sudo mkbox k8s install \
  [--distro kubeadm|k3s] \
  [--version 1.35] \
  [--repo-mirror aliyun|official] \
  [--image-repository ...] \
  [--registry-mirror ... | --no-registry-mirror] \
  [--k3s-mirror]
```

- kubeadm：安装 containerd，配置 CRI，写入 Kubernetes apt/yum 仓库，安装 kubelet/kubeadm/kubectl。
- k3s：通过官方或 Rancher 中国镜像脚本下载，跳过自动启动，写入 `registries.yaml`。

### `mkbox k8s init`

```bash
sudo mkbox k8s init [k8s install + up 的参数]
```

组件已安装时执行：

- kubeadm preflight 与 init，生成 `/etc/makebox/kubeadm-init.yaml`。
- 根据版本选择 `v1beta3` 或 `v1beta4` 配置 API。
- 导出 kubeconfig 到调用用户和 root。
- 保存 join 命令到 `/etc/makebox/join.sh`。
- 按 `--cni` 自动安装 flannel 或 calico（`--skip-addons` 可关闭）。
- 等待节点 Ready。

### `mkbox k8s up`

`k8s install` + `k8s init` 的快捷方式，额外参数与 `init` 相同。

常用参数：

| 参数 | 说明 |
| --- | --- |
| `--distro kubeadm|k3s` | 发行版 |
| `--version 1.35` | Kubernetes 版本，可写 `v1.35.2`；k3s 也接受 `v1.35.2+k3s1` 或版本通道 `1.35` |
| `--cni flannel|calico|none` | CNI，默认取配置 |
| `--pod-cidr` / `--service-cidr` | 集群网段 |
| `--apiserver-address` | 节点 IP，默认自动检测 |
| `--control-plane-endpoint vip:6443` | 控制面 VIP/域名 |
| `--image-repository` | Kubernetes 镜像仓库 |
| `--registry-mirror` | Docker Hub 镜像，可重复 |
| `--repo-mirror official|aliyun` | 组件软件源 |
| `--skip-preflight` | 跳过系统预检 |
| `--skip-firewall` | 不放行防火墙端口 |
| `--selinux-permissive` | SELinux 转 permissive |
| `--skip-addons` | 不安装 CNI |
| `--gh-proxy` | GitHub 下载代理 |
| `--k3s-mirror` | 使用 Rancher 中国镜像 |
| `--k3s-cluster-init` | k3s 多控制面 |
| `--timeout` | 等待就绪超时 |

### `mkbox k8s join`

```bash
# kubeadm
sudo mkbox k8s join \
  --server 10.0.0.10:6443 \
  --token <token> \
  --ca-cert-hash sha256:<hash> \
  [--control-plane] [--certificate-key <key>] \
  [--cri-socket unix:///run/containerd/containerd.sock]

# 执行控制面生成的脚本
sudo mkbox k8s join --from-file /tmp/join.sh

# k3s
sudo mkbox k8s join --distro k3s \
  --server https://10.0.0.10:6443 \
  --token <node-token> \
  [--node-ip 10.0.0.11]
```

kubeadm 加入前会执行 preflight；`--skip-preflight` 可跳过。未指定 `--distro` 时自动检测本机是否已安装 k3s。

### `mkbox k8s join-command`

```bash
sudo mkbox k8s join-command [--distro kubeadm|k3s] [-o /tmp/join.sh]
```

控制面生成带 token 和 CA hash 的命令；`-o` 保存为可执行脚本。

### `mkbox k8s addon`

```bash
mkbox k8s addon list
sudo mkbox k8s addon install <名称> [--kubeconfig ...] [--source <URL|文件>] [--gh-proxy ...] [--pod-cidr ...] [--timeout 3m]
sudo mkbox k8s addon apply <文件|URL> [--kubeconfig ...] [--gh-proxy ...] [--pod-cidr ...] [--timeout 3m]
```

内置插件：`flannel`、`calico`、`metrics-server`、`ingress-nginx`、`dashboard`、`local-path`。

- `metrics-server` 会自动加入 `--kubelet-insecure-tls`。
- `calico` 会按 `--pod-cidr` 修改 `CALICO_IPV4POOL_CIDR`。
- `--source` 可使用本地清单或自定义 URL。

### `mkbox k8s kubeconfig`

```bash
mkbox k8s kubeconfig [--source <路径>] [-o <路径|->] [--server https://ip:6443] [--force]
```

自动检测 `/etc/kubernetes/admin.conf`、`/etc/rancher/k3s/k3s.yaml` 或 `~/.kube/config`，修正 server 地址并设置 `0600` 权限。

### `mkbox k8s status`

```bash
mkbox k8s status [--kubeconfig <路径>]
```

显示节点、kube-system Pod 和关键组件状态。

### `mkbox k8s reset`

```bash
sudo mkbox k8s reset [--distro kubeadm|k3s] [--all]
```

- kubeadm：执行 `kubeadm reset` 并清理配置。
- k3s：停止并禁用 `k3s`/`k3s-agent` 服务后清理配置，默认保留 `/var/lib/rancher/k3s` 集群数据。
- `--all` 额外删除集群数据（kubeadm 的 `/var/lib/etcd`、k3s 的 `/var/lib/rancher/k3s`），不可恢复。
- 会要求确认；`--dry-run` 可预览。

## 收敛与回滚

以下命令把 Docker / Kubernetes 的期望状态与主机现状进行比较。期望状态来自配置文件（`registry.mirrors`、`docker.dataRoot`、`k8s.*`、`proxy.*` 等），不会读取或修改隐藏状态文件。

### `mkbox plan`

```bash
mkbox plan [docker|k8s|all] [--json] [--skip-preflight]
```

只读输出动作、风险、是否可回滚以及受管文件差异。`--json` 中的代理密码会替换为 `REDACTED`。scope 默认为 `all`。

### `mkbox diff`

```bash
mkbox diff [docker|k8s|all] [--json] [--exit-code]
```

只显示受管文件的 unified diff。使用 `--exit-code` 时，存在差异返回 `2`，适合 CI 或巡检脚本：

```bash
mkbox diff all --exit-code || [ $? -eq 2 ]
```

### `mkbox apply`

```bash
sudo mkbox apply [docker|k8s|all] \
  [--auto-rollback] [--state-dir /var/lib/mkbox] \
  [--skip-preflight] [--force] [--json]
```

按 `plan` 的动作执行，并在每个动作前把受管文件快照写入 `<state-dir>/journals/<id>.json`（默认权限 `0600`）。包含安装、集群初始化等不可自动撤销的动作时，必须显式加 `--force`。`--auto-rollback` 会在任一步失败时自动恢复已完成动作中可回滚的文件。

`--dry-run` 只打印计划，不执行动作，也不会创建 journal。

### `mkbox rollback`

```bash
sudo mkbox rollback [journal-id] [--state-dir /var/lib/mkbox] [--force] [--dry-run] [--json]
```

不带 ID 时回滚最近一次 journal。恢复快照中的文件，并仅重启事务前处于运行状态的服务。安装软件包、初始化集群等不可逆动作需要 `--force` 才会继续，且 mkbox **不会**自动卸载软件包或重置集群。`--dry-run` 仅列出将恢复/删除/跳过的文件。

安全限制：

- 只恢复 `/etc` 下的路径；journal 中的越界路径会被跳过。
- journal ID 经过白名单校验，拒绝 `..` 与路径分隔符。
- 已回滚的 journal 不能重复回滚。
- 快照超过 2 MiB 的文件只记录元数据，回滚时跳过并告警。

### `mkbox report`

```bash
mkbox report [--json] [-o report.json] [--state-dir /var/lib/mkbox]
```

生成环境证据 JSON，包含主机指纹、完整计划、运行时共存拓扑和 journal 列表。写入文件时权限为 `0600`，代理凭证已脱敏。适合作为工单附件或巡检留档。

## 运行时共存

### `mkbox runtime detect`

```bash
mkbox runtime detect [--json]
```

只读检测 cgroup v1/v2、Docker、containerd、cri-dockerd、kubelet、k3s、CRI socket、iptables 后端、swap 与已占用端口。代理 URL 默认脱敏。

### `mkbox runtime plan`

```bash
mkbox runtime plan [--json] [--strict]
```

在事实检测之上给出拓扑判断与整改建议，例如：

- Docker 自带 containerd 与系统 containerd 的 socket 冲突
- kubelet 使用 `unix:///var/run/dockershim.sock` 等已移除端点
- k8s 与 Docker 使用不同 cgroup driver
- 6443/10250/2375 等端口被非预期进程占用
- swap 仍启用、iptables 后端混用

`--strict` 在存在 error 级发现时返回非零退出码。

## 离线 bundle 与 OCI artifact

### `mkbox bundle build`

```bash
mkbox bundle build \
  --name mkbox-offline --version 0.2.0 \
  --platform linux/amd64 --license MIT \
  --source https://github.com/makebox/makebox --commit <sha> \
  --created-at 2026-01-01T00:00:00Z \
  --key ./mkbox-bundle.key \
  --file ./dist/mkbox \
  --file ./dist/mkbox-linux-amd64.tar.gz \
  -o ./mkbox-offline.mkbox.tar.gz [--json]
```

产物是确定性 tar.gz：条目排序、属主固定为 root、时间戳固定，包含：

- `manifest.json`：名称、版本、平台、每个文件的 SHA-256、大小与权限
- `blobs/sha256/<digest>`：文件内容
- `sbom.json`：SPDX 2.3 文档
- `checksums.txt`：所有文件的摘要
- `signature.json`：使用 `--key` 时的 Ed25519 签名（对 manifest 签名）

同一组输入、`--created-at` 与密钥会生成字节完全一致的文件。也可通过 `SOURCE_DATE_EPOCH` 固定时间。

### `mkbox bundle verify`

```bash
mkbox bundle verify <bundle.tar.gz> [--key ./mkbox-bundle.pub] [--require-signature] [--allow-unsigned] [--json]
```

流式校验 gzip/tar 结构、文件路径、重复条目、大小、摘要、SBOM、checksums 与签名。SBOM 与 checksums 缺失会直接判为失败。默认要求产物已签名；仅本地构建产物可用 `--allow-unsigned` 接受未签名 bundle。指定 `--key` 时还要求签名者与可信公钥一致。路径穿越、绝对路径、损坏的 gzip 和摘要不匹配都会返回非零退出码。

### `mkbox bundle inspect`

```bash
mkbox bundle inspect <bundle.tar.gz> [--json]
```

读取 manifest、SPDX SBOM 与签名信息，不校验文件内容。

### `mkbox bundle keygen`

```bash
mkbox bundle keygen [--out-dir ./keys] [--force] [--json]
```

生成 Ed25519 密钥：私钥 `mkbox-bundle.key`（`0600`），公钥 `mkbox-bundle.pub`（`0644`）。已存在时需 `--force`。

### `mkbox bundle push|pull`

```bash
mkbox bundle push <bundle.tar.gz> --ref <registry>/<repo>:<tag> \
  [--user <用户名> --password-stdin] [--plain-http] [--skip-verify] [--allow-unsigned] [--json]

mkbox bundle pull --ref <registry>/<repo>:<tag> [-o <文件>] \
  [--user <用户名> --password-stdin] [--plain-http] [--skip-verify] [--allow-unsigned] [--json]
```

bundle 会作为 OCI artifact 推送：config 为 `{}`，bundle 本身作为 layer，`artifactType` 为 `application/vnd.mkbox.bundle.v1`。推送前默认校验 bundle，拉取后默认再次校验并核对大小与摘要。回环地址自动使用 HTTP；其他地址默认 HTTPS，`--plain-http` 仅用于可信内网。

凭据也可通过 `MKBOX_REGISTRY_USER` / `MKBOX_REGISTRY_PASSWORD` 提供。`--password-stdin` 从标准输入读取密码，避免出现在进程参数中。

## Shell 自动补全

```bash
# bash
source <(mkbox completion bash)

# zsh
mkbox completion zsh > "${fpath[1]}/_mkbox"

# fish
mkbox completion fish | source

# PowerShell
mkbox completion powershell | Out-String | Invoke-Expression
```
