# 架构与设计

## 目标

`mkbox` 是一个单文件、静态编译的 Linux CLI。它不依赖 Python、Ansible 或远程 Agent，也不维护常驻服务。所有操作都在本机通过系统包管理器、systemd 和服务配置完成，适合：

- 新机器初始化
- 内网/离线环境的标准化安装
- 单节点开发集群
- 通过 SSH 批量执行的轻量运维

## 分层

```text
cmd/mkbox
  └── internal/cli        Cobra 命令树、参数绑定、交互确认、输出
        ├── internal/config      持久化配置
        ├── internal/system      发行版检测、原子文件写入、备份
        ├── internal/execx       命令执行与 dry-run
        ├── internal/i18n        中英文消息目录与语言选择
        ├── internal/logx        彩色输出与日志
        ├── internal/docker      Docker 安装、daemon.json
        ├── internal/containerd  containerd 配置、certs.d
        ├── internal/k8s         preflight/init/join/addon/reset
        ├── internal/proxy       系统与 apt 代理
        ├── internal/mirror      软件源与开发工具镜像
        ├── internal/registry    镜像探测
        ├── internal/backup      配置快照
        ├── internal/doctor      只读体检
        ├── internal/reconcile   期望状态、计划、journal、apply/rollback
        ├── internal/coexist     运行时共存检测与拓扑建议
        └── internal/bundle      确定性归档、SBOM、签名、OCI 客户端
```

依赖方向从 CLI 指向领域包；领域包之间只有少量复用（例如 Docker 的代理渲染被 containerd 复用），避免循环依赖。

## 命令执行模型

每个命令共享一个 `App`：

```go
type App struct {
    Build     BuildInfo
    OS        *system.OS
    Cfg       *config.Config
    CfgPath   string
    Lang      string
    DryRun    bool
    AssumeYes bool
    Verbose   bool
    R         *execx.Runner
    FS        *system.FS
}
```

- `PersistentPreRunE` 检测发行版、初始化 `Runner` 和 `FS`，并加载配置。配置加载失败不会阻止 `config` / `doctor` 等命令运行。
- 语言在构建 Cobra 命令树之前确定：`Execute` 预扫描 `--lang` 与 `--config`，再依次回退到 `MKBOX_LANG`、配置文件 `language` 和 `zh-CN`。随后 `localizeCommands` 就地翻译 `Use`、`Short`、`Long`、`Example`、参数说明和分组标题。
- `execx.Runner` 统一处理 `--dry-run`、`--verbose`、stdin/stdout/stderr。dry-run 时只打印命令，不实际执行。
- `system.FS` 统一处理写文件：创建父目录、备份原文件、写临时文件、`rename`。dry-run 时打印目标路径和内容预览。

## 国际化

`internal/i18n` 以中文原文作为消息键，避免为每条消息维护额外 ID：

- `logx` 在输出前调用 `i18n.Translate`，格式字符串中的 `%s`/`%d` 保持不变。
- 英文目录未收录的键回退为中文，动态拼接的错误文本因此仍可读。
- 默认语言为 `zh-CN`，不自动读取 `LANG`，避免改变脚本与测试的既有输出。
- `--lang`、`MKBOX_LANG`、配置 `language` 三处入口共用 `i18n.Normalize`，支持 `zh`、`zh-CN`、`en`、`en-US` 等写法并归一化。

## 安全写入

```text
目标文件存在?
  ├── 是 → 复制为 <path>.mkbox.bak.<timestamp>
  └── 否 → 跳过备份
写入 <path>.tmp.<random>
fsync / close
rename(<path>.tmp, <path>)
chmod
```

设计取舍：

- 不做跨文件事务。多个文件写入时，每一步都有独立备份，失败后可按时间回滚。
- `daemon.json` 使用 `map[string]any` 读取和合并，未知键原样保留。
- 恢复备份时再走一次 `FS.Write`，因此覆盖前还会生成新的逐文件备份。
- 恢复归档时拒绝绝对路径和 `..`，跳过链接和特殊文件，单文件上限 512 MiB。

## 发行版适配

`system.Detect()` 读取 `/etc/os-release`，将发行版归类为：

| family | 包管理器 | 服务管理器 |
| --- | --- | --- |
| `debian` | apt | systemd（或容器内无） |
| `rhel` | dnf/yum | systemd |
| `suse` | zypper | systemd |
| `arch` | pacman | systemd |
| `alpine` | apk | openrc |

Docker 安装按 family 分派；kubeadm 安装只覆盖 `debian` 与 `rhel`，其他家族引导使用 k3s。软件源切换只对 Debian/Ubuntu、Arch、Alpine 自动执行，避免破坏 RHEL/SUSE 的订阅仓库。

## Kubernetes 流程

### kubeadm

```text
preflight
  ├── swap off / fstab
  ├── modules + sysctl
  ├── containerd SystemdCgroup / pause / certs.d
  └── firewall + SELinux
install
  ├── containerd
  ├── Kubernetes apt/yum 仓库
  └── kubelet/kubeadm/kubectl
init
  ├── 生成 KubeadmOptions
  ├── 按版本选择 v1beta3/v1beta4
  ├── kubeadm config images pull
  ├── kubeadm init
  ├── 导出 kubeconfig
  ├── 保存 join.sh
  └── 安装 CNI + 等待 Ready
```

kubeadm 配置采用 YAML 字符串渲染而不是引入 Kubernetes 依赖，保持二进制体积和依赖树可控。`certSANs` 自动包含 hostname、节点 IP 和 control-plane-endpoint 主机名。

### k3s

```text
install
  ├── 下载 k3s（官方或 Rancher 中国镜像）
  ├── INSTALL_K3S_SKIP_START=true
  └── 写 registries.yaml
init
  ├── 生成 config.yaml（cluster-init、tls-san、node-ip 等）
  ├── 启动 k3s
  └── 导出 kubeconfig
```

k3s join 通过 `k3s agent` 的 systemd 服务或安装脚本参数完成，token 从控制面 `/var/lib/rancher/k3s/server/node-token` 读取。

## 镜像加速

`internal/registry` 只负责探测，不做决策：

```go
Normalize  → 补 https、去尾斜杠、拒绝非 http(s)
Dedupe     → 保序去重
Check      → GET <mirror>/v2/，200/401 视为可用
CheckAll   → 并发探测
BestURLs   → 按延迟排序，失败项排除
```

写入层分别处理：

- Docker：`/etc/docker/daemon.json` 的 `registry-mirrors`
- containerd：`/etc/containerd/certs.d/docker.io/hosts.toml`
- k3s：`/etc/rancher/k3s/registries.yaml`

`registry.maxMirrors` 限制实际写入数量，避免一次写入过多候选导致拉取时逐个超时。

## 代理

代理配置按 scope 分开：

- `system`：`/etc/environment`，重写受管键，保留其他变量
- `shell`：`/etc/profile.d/mkbox-proxy.sh`
- `apt`：`/etc/apt/apt.conf.d/95mkbox-proxy`
- `docker` / `containerd`：systemd drop-in，同时设置大小写变量

代理 URL 通过 `net/url` 校验，要求 `http`/`https`/`socks5`/`socks5h`，拒绝路径。密码在 `config show` 中默认遮盖。

## 收敛引擎

`internal/reconcile` 把命令式安装流程拆成可观察的四个阶段：

```text
ProfileFromConfig(scope)        期望状态（镜像、data-root、K8s 参数、代理）
        +
Observe(host)                   只读事实（文件内容、服务状态、已安装组件）
        ↓
BuildPlan                       Actions + FileDiffs + Warnings
        ↓
Apply                           逐动作快照 → 执行 → 记录 journal
        ↓
Rollback                        反向恢复文件，重启事务前活跃服务
```

设计要点：

- `Observe` 不会被 `--dry-run` 关闭，否则计划恒为空；它只读文件与 `systemctl is-active`。
- 计划中的动作有稳定 ID（`docker.install`、`docker.configure`、`k8s.init` 等），CLI 再将 ID 映射到既有安装代码，避免重写一遍安装逻辑。
- 文件差异使用受管文件的期望内容与当前内容直接比较；代理相关 diff 在生成时即脱敏。
- `apply` 的 journal 记录动作状态、文件快照、事务前服务状态和错误。快照路径必须位于 `/etc`，防止损坏的 journal 写入任意位置。
- `JournalStore` 使用临时文件 + `rename`，目录 `0700`、文件 `0600`。回滚拒绝重复执行，并对不可逆动作要求 `--force`。
- `diff --exit-code` 用退出码 `2` 表示漂移，便于与“命令失败”（`1`）区分。

## 运行时共存

`internal/coexist` 只做事实检测与建议，不修改系统。检测项包括：

- Docker / containerd / cri-dockerd / kubelet / k3s 的安装与运行状态
- containerd `SystemdCgroup`、Docker cgroup driver、cgroup v1/v2
- CRI socket 列表与 kubelet 配置中的端点
- iptables/nft 后端、swap、6443/10250/2375 等端口占用
- 代理环境变量（输出时脱敏）

`BuildPlan` 将事实转换为带 severity 的 findings 与 next steps；`--strict` 在存在 error 级发现时返回失败。这样用户可以先理解拓扑，再决定是否调整，而不是让诊断工具自动改配置。

## 离线 bundle 与 OCI

`internal/bundle` 不依赖外部打包工具：

```text
输入文件 → 排序 → 计算摘要 → blobs/sha256/<hex>
        → manifest.json（文件名、大小、权限、媒体类型）
        → sbom.json（SPDX 2.3）
        → checksums.txt
        → signature.json（可选，Ed25519 对 manifest 签名）
        → deterministic tar.gz
```

确定性来自固定条目顺序、uid/gid、mtime、gzip 头与压缩级别；`SOURCE_DATE_EPOCH` 或 `--created-at` 固定构建时间，因此相同输入和密钥可字节级复现。

校验器流式读取 tar，拒绝绝对路径、`..`、重复条目、设备文件、大小或摘要不匹配，并检查 manifest、SBOM、checksums 与签名的一致性。私钥为 PKCS#8 PEM（`0600`），公钥为 PKIX PEM（`0644`）。

`internal/bundle/oci.go` 实现最小 OCI distribution 客户端：

- `HEAD` blob 去重，`POST` 创建上传会话，`PUT` 单请求上传
- manifest 使用 `artifactType=application/vnd.mkbox.bundle.v1`
- 解析 `WWW-Authenticate: Bearer` challenge，缓存 token，并只在同 host 重试
- 拉取时边写临时文件边计算 SHA-256，校验大小、digest 和注释中的 bundle 摘要后再原子改名
- 回环 registry 自动走 HTTP；其他地址默认 HTTPS

## 配置

配置是 JSON，默认路径：

```text
$MKBOX_CONFIG
$XDG_CONFIG_HOME/makebox/config.json
~/.config/makebox/config.json
```

加载时对缺失字段应用默认值，但保留显式空列表（例如 `"mirrors": []`）。保存使用临时文件 + `rename`，权限 `0600`。`language` 默认为 `zh-CN`，可选 `en`。

## 可扩展点

- 新发行版：在 `system.Classify` 增加映射，在 Docker/Kubernetes 安装分派中增加分支。
- 新 CNI/插件：在 `k8s.addonDefs` 增加定义，必要时在 `patch` 中处理清单差异。
- 新镜像源：在 `mirror.Sources` 增加条目；`DetectOSMirror` 会自动识别。
- 新备份项：在 `backup.DefaultPaths` 增加路径。
- 新体检项：在 `doctor.Run` 增加检查函数，使用 `ok/warn/fail/info` 级别。
- 新收敛动作：在 `reconcile.BuildPlan` 增加 Action，在 CLI 的 `reconcileExecutor` 映射执行逻辑，并补充 `ManagedFilesForAction`。
- 新运行时规则：在 `coexist.BuildPlan` 增加 Finding 与 next step。
- 新 bundle 媒体类型或分发后端：扩展 `internal/bundle`，保持 manifest 与校验器同步。

## 测试策略

- 纯函数优先：kubeadm 配置渲染、containerd 配置编辑、Calico/metrics-server 补丁、代理文件渲染、软件源渲染、配置校验。
- 网络探测使用 `httptest`，覆盖 200/401/500 和延迟排序。
- 文件操作使用 `t.TempDir()`，覆盖备份/恢复、路径穿越、dry-run、原子写入。
- 收敛引擎覆盖从零/已收敛/已有集群的计划、daemon.json 合并、journal 权限、路径越界、回滚恢复/删除/不可逆动作与自动回滚。
- bundle 覆盖字节级可复现、签名/可信公钥、篡改、tar 穿越、保留文件名、密钥权限，以及带 Bearer challenge 的假 OCI registry 推送/拉取与损坏 blob 拒绝。
- 严格冒烟脚本对发布产物再跑一遍 CLI 级 plan/diff/report/apply dry-run/rollback dry-run、bundle 构建校验与本地 registry 推拉。
- 不依赖真实 Docker/Kubernetes 的单元测试可以在 CI 中直接运行；端到端验证需要 root 与虚拟机。
