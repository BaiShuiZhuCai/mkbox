package i18n

// englishCommands translates command Short/Long/Example text and group titles.
var englishCommands = map[string]string{
	// Usage argument placeholders.
	"restore <快照>":   "restore <snapshot>",
	"get <键>":        "get <key>",
	"set <键> <值>":    "set <key> <value>",
	"set [镜像地址...]":  "set [mirror...]",
	"set <代理地址>":     "set <proxy-url>",
	"data-root <目录>": "data-root <directory>",
	"install <插件名>":  "install <addon>",
	"apply <文件|URL>": "apply <file|URL>",
	"os [镜像站]":       "os [mirror]",
	"test [镜像地址...]": "test [mirror...]",

	// Root and help.
	"一键配置 Docker 与 Kubernetes 的 Linux 运维工具": "One-command Docker and Kubernetes operations tool for Linux",
	"makebox（命令名 mkbox）面向主流 Linux 发行版，提供 Docker、Kubernetes、\n镜像加速、系统代理、软件源与配置备份的一站式操作。\n\n常用流程：\n  sudo mkbox doctor                 # 环境体检\n  sudo mkbox plan                   # 查看期望状态与现状差异（只读）\n  sudo mkbox apply --auto-rollback  # 事务化收敛，失败自动回滚\n  sudo mkbox docker install         # 安装 Docker 并配置镜像加速\n  sudo mkbox k8s up                 # 安装并初始化单节点 Kubernetes\n  sudo mkbox k8s join ...           # 将工作节点加入集群\n  mkbox bundle build --file ...     # 构建可复现的签名离线 bundle\n\n支持 Debian/Ubuntu、RHEL/Rocky/Alma/Fedora、openSUSE、Arch、Alpine。\n所有修改都会先备份原文件，并支持 --dry-run 预览。": "makebox (command name: mkbox) targets mainstream Linux distributions with\none-stop Docker, Kubernetes, registry mirror, system proxy, package mirror\nand configuration backup operations.\n\nCommon workflows:\n  sudo mkbox doctor                 # health check\n  sudo mkbox plan                   # show desired vs. actual state (read-only)\n  sudo mkbox apply --auto-rollback  # transactional reconcile with rollback\n  sudo mkbox docker install         # install Docker and configure mirrors\n  sudo mkbox k8s up                 # install and initialize single-node Kubernetes\n  sudo mkbox k8s join ...           # join a worker node to the cluster\n  mkbox bundle build --file ...     # build a reproducible signed offline bundle\n\nSupports Debian/Ubuntu, RHEL/Rocky/Alma/Fedora, openSUSE, Arch and Alpine.\nAll changes back up original files first and support --dry-run previews.",
	"查看任意命令的帮助": "Show help for any command",
	"查看 mkbox 或任意子命令的详细帮助。例如：mkbox help k8s init。": "Show detailed help for mkbox or any subcommand. Example: mkbox help k8s init.",
	"显示版本与构建信息":   "Show version and build information",
	"基础工具：":       "Core tools:",
	"Docker 与系统：": "Docker & system:",
	"Kubernetes：": "Kubernetes:",
	"收敛与供应链：":     "Reconcile & supply chain:",

	// Backup.
	"备份与恢复 mkbox 管理的配置": "Back up and restore mkbox-managed configuration",
	"创建配置快照（tar.gz）":    "Create a configuration snapshot (tar.gz)",
	"默认备份 Docker、containerd、Kubernetes、apt、代理与 mkbox 配置，不包含镜像和 etcd 数据。": "Backs up Docker, containerd, Kubernetes, apt, proxy and mkbox configuration by default; images and etcd data are excluded.",
	"列出已有快照": "List existing snapshots",
	"从快照恢复配置（覆盖前会逐文件备份）": "Restore configuration from a snapshot (each file is backed up before overwrite)",

	// Bundle.
	"构建、签名、校验并分发离线 bundle（OCI artifact）": "Build, sign, verify and distribute offline bundles (OCI artifact)",
	"构建确定性 tar.gz bundle（可选 Ed25519 签名）": "Build a deterministic tar.gz bundle (optional Ed25519 signature)",
	"将若干文件打包为可复现的 tar.gz：条目排序、固定属主与时间戳，\n包含 manifest.json、checksums.txt 和 SPDX SBOM。使用 --key 时对 manifest 签名。\n设置 SOURCE_DATE_EPOCH 或 --created-at 可让多次构建字节完全一致。": "Package files into a reproducible tar.gz: sorted entries, fixed ownership and\ntimestamps, with manifest.json, checksums.txt and an SPDX SBOM. --key signs the\nmanifest. Set SOURCE_DATE_EPOCH or --created-at for byte-identical rebuilds.",
	"校验摘要、checksums 与 Ed25519 签名":           "Verify digests, checksums and the Ed25519 signature",
	"查看 bundle 清单、SBOM 与签名信息":               "Show the bundle manifest, SBOM and signature details",
	"生成 Ed25519 签名密钥（私钥 0600）":              "Generate an Ed25519 signing key (private key mode 0600)",
	"将 bundle 作为 OCI artifact 推送到 registry": "Push a bundle to a registry as an OCI artifact",
	"从 registry 拉取 bundle 并校验摘要":            "Pull a bundle from a registry and verify its digest",

	// Completion.
	"生成 shell 自动补全脚本":                   "Generate a shell completion script",
	"生成 mkbox 的 shell 自动补全脚本，并输出到标准输出。": "Generate a shell completion script for mkbox and write it to stdout.",

	// Config.
	"查看与修改 mkbox 配置":   "View and modify mkbox configuration",
	"显示完整配置（默认隐藏代理密码）": "Show the full configuration (proxy passwords hidden by default)",
	"读取一个配置项":          "Read a configuration key",
	"修改一个配置项":          "Modify a configuration key",
	"显示配置文件路径":         "Show the configuration file path",
	"生成默认配置文件":         "Generate the default configuration file",

	// Docker.
	"安装、卸载与配置 Docker Engine":            "Install, uninstall and configure Docker Engine",
	"安装 Docker Engine 并配置镜像加速与日志轮转":     "Install Docker Engine and configure registry mirrors and log rotation",
	"卸载 Docker Engine":                  "Uninstall Docker Engine",
	"显示 Docker 安装与运行状态":                 "Show Docker installation and runtime status",
	"管理 Docker Hub 镜像加速器":               "Manage Docker Hub registry mirrors",
	"写入 registry-mirrors 并重启 Docker":    "Write registry-mirrors and restart Docker",
	"不带地址时重新应用配置中的镜像列表。地址会自动规范化并去重。":    "Without addresses, re-applies the configured mirror list. Addresses are normalized and de-duplicated.",
	"移除 Docker 镜像加速配置并重启 Docker":        "Remove Docker registry mirrors and restart Docker",
	"显示配置与 daemon.json 中的镜像加速器":         "Show mirrors from the configuration and daemon.json",
	"显示完整的 daemon.json":                 "Show the complete daemon.json",
	"测试当前 daemon.json 中的镜像加速器":          "Test the mirrors in the current daemon.json",
	"为 Docker daemon 配置 HTTP(S) 代理":     "Configure an HTTP(S) proxy for the Docker daemon",
	"通过 systemd drop-in 为 dockerd 设置代理": "Set the dockerd proxy through a systemd drop-in",
	"移除 Docker daemon 代理并重启":            "Remove the Docker daemon proxy and restart",
	"显示 Docker daemon 代理配置状态":           "Show Docker daemon proxy status",
	"修改 Docker 数据目录（不会迁移已有数据）":          "Change the Docker data root (existing data is not migrated)",

	// Doctor.
	"检查系统是否满足 Docker / Kubernetes 安装条件":                                 "Check whether the system meets Docker / Kubernetes requirements",
	"只读体检：发行版、内核、资源、内核模块、防火墙、Docker、containerd、Kubernetes、配置与可选的网络连通性。": "Read-only health check: distribution, kernel, resources, kernel modules, firewall, Docker, containerd, Kubernetes, configuration and optional network connectivity.",

	// Kubernetes.
	"安装、初始化与运维 Kubernetes 集群":                             "Install, initialize and operate a Kubernetes cluster",
	"关闭 swap、加载模块、配置 sysctl 与防火墙":                         "Disable swap, load modules, configure sysctl and the firewall",
	"安装 kubeadm/kubelet/kubectl 或 k3s":                    "Install kubeadm/kubelet/kubectl or k3s",
	"初始化 Kubernetes 控制平面（组件需已安装）":                         "Initialize the Kubernetes control plane (components must be installed)",
	"一键安装并初始化单节点 Kubernetes":                              "Install and initialize single-node Kubernetes in one step",
	"等价于 mkbox k8s install 后执行 mkbox k8s init，并自动安装 CNI。": "Equivalent to mkbox k8s install followed by mkbox k8s init, including automatic CNI installation.",
	"将当前节点加入已有集群":                                         "Join the current node to an existing cluster",
	"kubeadm 节点需要 --server、--token、--ca-cert-hash，或使用 --from-file 执行控制面生成脚本。\nk3s 节点使用 --distro k3s --server https://<server>:6443 --token <token>。": "kubeadm nodes need --server, --token and --ca-cert-hash, or --from-file to run\na control-plane generated script. k3s nodes use --distro k3s --server\nhttps://<server>:6443 --token <token>.",
	"在控制面生成加入集群的命令":                   "Generate the join command on the control plane",
	"安装 Kubernetes 常用插件":              "Install common Kubernetes addons",
	"列出内置插件":                          "List built-in addons",
	"下载并应用插件清单":                       "Download and apply the addon manifest",
	"应用自定义 Kubernetes 清单":             "Apply a custom Kubernetes manifest",
	"导出 kubeconfig 并修正 API Server 地址": "Export kubeconfig and correct the API Server address",
	"查看集群节点与组件状态":                     "Show cluster node and component status",
	"重置本机 Kubernetes（删除配置与运行数据）":      "Reset local Kubernetes (deletes configuration and runtime data)",

	// Mirror.
	"切换 Linux 软件源与开发工具镜像":                                   "Switch Linux package mirrors and developer tool mirrors",
	"切换系统软件源（aliyun/tuna/ustc/huawei）":                      "Switch the system package mirror (aliyun/tuna/ustc/huawei)",
	"支持 Ubuntu/Debian、Arch、Alpine。RHEL/SUSE 因仓库结构差异暂不自动切换。": "Supports Ubuntu/Debian, Arch and Alpine. RHEL/SUSE are not switched automatically because their repository layouts differ.",
	"配置 pip / npm / Go 模块镜像":                                "Configure pip / npm / Go module mirrors",
	"默认同时配置三者；也可用 --pip、--npm、--go 精细选择。":                   "Configures all three by default; use --pip, --npm or --go to select specific ones.",
	"列出支持的镜像站":                                              "List supported mirrors",
	"显示当前软件源镜像站":                                            "Show the current package mirror",

	// Net and registry.
	"网络连通性检查": "Check network connectivity",
	"测试 Docker Hub、Kubernetes、GitHub、镜像站等端点": "Test endpoints including Docker Hub, Kubernetes, GitHub and mirrors",
	"容器镜像加速器管理":                              "Manage container registry mirrors",
	"列出已配置的镜像加速器":                            "List configured registry mirrors",
	"测试镜像加速器的 /v2/ 接口与延迟":                    "Test registry mirror /v2/ endpoints and latency",
	"不带参数时测试配置中的镜像；也可直接传入一个或多个地址。":           "Without arguments, tests the configured mirrors; one or more addresses may also be passed.",

	// Proxy.
	"配置系统、apt、Docker 与 containerd 代理": "Configure system, apt, Docker and containerd proxies",
	"设置 HTTP(S)/SOCKS5 代理":            "Set an HTTP(S)/SOCKS5 proxy",
	"支持的 scope：system、shell、apt、docker、containerd、all。\n默认全部启用；未安装的 Docker/containerd 会自动跳过。": "Supported scopes: system, shell, apt, docker, containerd, all.\nAll are enabled by default; missing Docker/containerd installations are skipped.",
	"移除系统、apt、Docker 与 containerd 代理": "Remove system, apt, Docker and containerd proxies",
	"显示当前代理配置":                        "Show the current proxy configuration",

	// Reconcile.
	"只读比较期望状态与主机现状": "Compare desired state with the host (read-only)",
	"根据配置计算需要执行的动作和文件差异，不会修改主机。\nscope 默认为 all；可用配置文件调整镜像加速、Kubernetes 版本等期望状态。": "Computes required actions and file differences from the configuration without\nmodifying the host. scope defaults to all; the configuration file controls\nmirrors, Kubernetes version and other desired state.",
	"只显示受管文件的差异":          "Show only differences in managed files",
	"按计划收敛主机状态（带事务日志与回滚）": "Reconcile the host to the plan (with transactional journal and rollback)",
	"执行 plan 计算出的动作。每个动作执行前会快照受管文件并写入\n/var/lib/mkbox/journals，失败时可用 mkbox rollback 恢复。": "Executes the actions computed by plan. Before each action, managed files are\nsnapshotted into /var/lib/mkbox/journals; use mkbox rollback to restore after a\nfailure.",
	"回滚一次 apply 事务": "Roll back an apply transaction",
	"恢复 journal 中快照的受管文件，并重启事务前处于运行状态的服务。\n软件包安装、集群初始化等不可逆动作需要 --force 才会继续（仍不会自动卸载/重置集群）。": "Restores managed files from the journal snapshot and restarts services that were\nrunning before the transaction. Irreversible actions such as package installation\nand cluster initialization require --force (packages are never uninstalled and\nclusters are never reset automatically).",
	"检测 Docker/containerd/Kubernetes 运行时共存关系": "Detect Docker/containerd/Kubernetes runtime coexistence",
	"只读输出运行时事实":                               "Print runtime facts (read-only)",
	"给出运行时共存拓扑与整改建议":                          "Show the runtime coexistence topology and remediation advice",
	"生成可归档的环境证据 JSON（计划+运行时+journal）":         "Generate archivable environment evidence JSON (plan + runtime + journal)",
}
