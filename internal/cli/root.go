// Package cli implements the mkbox command tree.
package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/makebox/makebox/internal/config"
	"github.com/makebox/makebox/internal/docker"
	"github.com/makebox/makebox/internal/execx"
	"github.com/makebox/makebox/internal/i18n"
	"github.com/makebox/makebox/internal/k8s"
	"github.com/makebox/makebox/internal/logx"
	"github.com/makebox/makebox/internal/registry"
	"github.com/makebox/makebox/internal/system"
)

// BuildInfo is injected at link time.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// App carries global flags and lazily detected host state.
type App struct {
	Build     BuildInfo
	OS        *system.OS
	Cfg       *config.Config
	CfgErr    error
	CfgPath   string
	Lang      string
	DryRun    bool
	AssumeYes bool
	Verbose   bool
	NoColor   bool
	R         *execx.Runner
	FS        *system.FS
}

// Execute runs the mkbox CLI.
func Execute(build BuildInfo) error {
	if build.Version == "" {
		build.Version = "dev"
	}
	lang, err := resolveLanguage(os.Args[1:])
	if err != nil {
		logx.Errorf("%v", err)
		return err
	}
	if err := i18n.SetLanguage(lang); err != nil {
		logx.Errorf("%v", err)
		return err
	}
	app := &App{Build: build, Lang: lang}
	return app.rootCmd().Execute()
}

func (a *App) rootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "mkbox",
		Short:   "一键配置 Docker 与 Kubernetes 的 Linux 运维工具",
		Version: a.Build.Version,
		Long: `makebox（命令名 mkbox）面向主流 Linux 发行版，提供 Docker、Kubernetes、
镜像加速、系统代理、软件源与配置备份的一站式操作。

常用流程：
  sudo mkbox doctor                 # 环境体检
  sudo mkbox plan                   # 查看期望状态与现状差异（只读）
  sudo mkbox apply --auto-rollback  # 事务化收敛，失败自动回滚
  sudo mkbox docker install         # 安装 Docker 并配置镜像加速
  sudo mkbox k8s up                 # 安装并初始化单节点 Kubernetes
  sudo mkbox k8s join ...           # 将工作节点加入集群
  mkbox bundle build --file ...     # 构建可复现的签名离线 bundle

支持 Debian/Ubuntu、RHEL/Rocky/Alma/Fedora、openSUSE、Arch、Alpine。
所有修改都会先备份原文件，并支持 --dry-run 预览。`,
		Example: `  mkbox doctor
  sudo mkbox docker install
  sudo mkbox k8s up --cni calico
  sudo mkbox k8s join-command
  sudo mkbox proxy set http://127.0.0.1:7890 --scope all`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			a.OS = system.Detect()
			if a.NoColor {
				logx.SetNoColor(true)
			}
			a.R = execx.New(a.DryRun, a.Verbose)
			a.FS = &system.FS{DryRun: a.DryRun}
			if a.CfgPath == "" {
				a.CfgPath = config.Path()
			}
			cfg, err := config.Load(a.CfgPath)
			a.Cfg, a.CfgErr = cfg, err
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runOverview()
		},
	}

	pf := cmd.PersistentFlags()
	pf.BoolVar(&a.DryRun, "dry-run", false, "只显示将要执行的修改，不写入系统")
	pf.BoolVarP(&a.AssumeYes, "yes", "y", false, "跳过交互确认")
	pf.BoolVar(&a.Verbose, "verbose", false, "输出调试信息与命令")
	pf.BoolVar(&a.NoColor, "no-color", false, "禁用彩色输出")
	pf.StringVar(&a.CfgPath, "config", "", "配置文件路径（默认 ~/.config/makebox/config.json）")
	pf.StringVar(&a.Lang, "lang", a.Lang, "界面语言: zh-CN 或 en（默认取 MKBOX_LANG、配置文件或 zh-CN）")

	cmd.AddGroup(
		&cobra.Group{ID: "core", Title: "基础工具："},
		&cobra.Group{ID: "docker", Title: "Docker 与系统："},
		&cobra.Group{ID: "k8s", Title: "Kubernetes："},
		&cobra.Group{ID: "reconcile", Title: "收敛与供应链："},
	)
	helpCmd := a.helpCmd(cmd)
	helpCmd.GroupID = "core"
	cmd.SetHelpCommand(helpCmd)
	cmd.SetCompletionCommandGroupID("core")
	localizeCommands(helpCmd)

	add := func(group string, cmds ...*cobra.Command) {
		for _, c := range cmds {
			c.GroupID = group
			cmd.AddCommand(c)
		}
	}
	add("core", a.doctorCmd(), a.netCmd(), a.registryCmd(), a.configCmd(), a.backupCmd(), a.versionCmd(), a.completionCmd())
	add("docker", a.dockerCmd(), a.proxyCmd(), a.mirrorCmd())
	add("k8s", a.k8sCmd())
	add("reconcile", a.planCmd(), a.diffCmd(), a.applyCmd(), a.rollbackCmd(), a.reportCmd(), a.runtimeCmd(), a.bundleCmd())
	localizeCommands(cmd)
	return cmd
}

func (a *App) helpCmd(root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:   "help [command]",
		Short: "查看任意命令的帮助",
		Long:  "查看 mkbox 或任意子命令的详细帮助。例如：mkbox help k8s init。",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			target, rest, err := root.Find(args)
			if err != nil {
				return err
			}
			if target == nil || len(rest) > 0 {
				return fmt.Errorf("未知命令: %s", strings.Join(args, " "))
			}
			return target.Help()
		},
	}
}

func (a *App) runOverview() error {
	logx.Banner(a.Build.Version)
	o := a.OS
	logx.Header("系统")
	logx.Item("发行版", o.DisplayName())
	logx.Item("内核", o.Kernel)
	logx.Item("架构", o.Arch)
	if o.IsRoot {
		logx.Item("权限", "root")
	} else {
		logx.Item("权限", "普通用户（修改系统需 sudo）")
	}

	logx.Header("Docker")
	st := docker.GetStatus(a.R)
	switch {
	case st.Installed && st.Running:
		logx.Item("状态", i18n.Tf("运行中 %s", strings.TrimSpace(st.Version)))
		logx.Item("镜像加速", i18n.Tf("%d 个", len(st.Mirrors)))
		if st.DataRoot != "" {
			logx.Item("数据目录", st.DataRoot)
		}
	case st.Installed:
		logx.Item("状态", "已安装，服务未运行")
	default:
		logx.Item("状态", "未安装")
	}

	logx.Header("Kubernetes")
	if kc := k8s.DetectClusterKubeconfig(); kc != "" {
		logx.Item("kubeconfig", kc)
		if out, err := a.R.Output("kubectl", "--kubeconfig", kc, "get", "nodes", "--no-headers"); err == nil && strings.TrimSpace(out) != "" {
			lines := strings.Split(strings.TrimSpace(out), "\n")
			ready := 0
			for _, line := range lines {
				fields := strings.Fields(line)
				if len(fields) > 1 && strings.HasPrefix(fields[1], "Ready") {
					ready++
				}
			}
			logx.Item("节点", i18n.Tf("%d 个，%d 个 Ready", len(lines), ready))
		}
	} else {
		logx.Item("状态", "未检测到本地集群")
	}

	logx.Header("mkbox")
	if a.CfgErr != nil {
		logx.Item("配置", i18n.Tf("加载失败: %s", a.CfgErr.Error()))
	} else if a.Cfg != nil {
		logx.Item("k8s 默认", fmt.Sprintf("%s / %s", a.Cfg.K8s.Distro, a.Cfg.K8s.Version))
		shown := a.Cfg.Registry.Mirrors
		if len(shown) > 3 {
			shown = shown[:3]
		}
		logx.Item("镜像加速", logx.JoinList(registry.Dedupe(shown)))
	}
	logx.Item("配置文件", a.CfgPath)
	logx.Raw("\n运行 `mkbox doctor` 检查环境，`mkbox --help` 查看全部命令。")
	return nil
}

// cfgOrErr returns the loaded configuration or the load error.
func (a *App) cfgOrErr() (*config.Config, error) {
	if a.CfgErr != nil {
		return nil, a.CfgErr
	}
	if a.Cfg == nil {
		return nil, fmt.Errorf("配置未初始化")
	}
	return a.Cfg, nil
}

func (a *App) saveConfig(cfg *config.Config) error {
	if a.DryRun {
		logx.Infof("[dry-run] 写入配置 %s", a.CfgPath)
		return nil
	}
	data, err := cfg.Marshal()
	if err != nil {
		return fmt.Errorf("保存配置 %s: %w", a.CfgPath, err)
	}
	if err := a.FS.EnsureDir(filepath.Dir(a.CfgPath), 0o700); err != nil {
		return fmt.Errorf("创建配置目录: %w", err)
	}
	if err := a.FS.Write(a.CfgPath, data, 0o600); err != nil {
		return fmt.Errorf("保存配置 %s: %w", a.CfgPath, err)
	}
	if err := os.Chmod(a.CfgPath, 0o600); err != nil {
		return fmt.Errorf("设置配置权限 %s: %w", a.CfgPath, err)
	}
	logx.Successf("已更新配置 %s", a.CfgPath)
	return nil
}

func (a *App) requireRoot() error {
	if a.DryRun || a.OS.IsRoot {
		return nil
	}
	return fmt.Errorf("此操作需要 root 权限，请使用 sudo 运行（只读命令或 --dry-run 除外）")
}

func (a *App) confirm(prompt string) (bool, error) {
	if a.AssumeYes || a.DryRun {
		return true, nil
	}
	if !logx.IsTerminal(os.Stdin) {
		return false, fmt.Errorf("非交互环境无法确认，请添加 --yes")
	}
	fmt.Fprintf(os.Stdout, "? %s [y/N]: ", prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		return false, fmt.Errorf("读取确认输入失败: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes", "是":
		return true, nil
	default:
		return false, nil
	}
}

func (a *App) registryMirrors(explicit []string, disabled bool) []string {
	if disabled {
		return nil
	}
	if len(explicit) > 0 {
		return registry.Dedupe(explicit)
	}
	if a.Cfg != nil {
		return registry.Dedupe(a.Cfg.Registry.Mirrors)
	}
	return registry.Dedupe(config.DefaultMirrors)
}

// appliedMirrors limits the candidate list to registry.maxMirrors, matching
// what mkbox writes into Docker/containerd configuration. Explicit entries are
// validated so a typo cannot be silently dropped.
func (a *App) appliedMirrors(explicit []string, disabled bool) ([]string, error) {
	if disabled {
		return nil, nil
	}
	if len(explicit) > 0 {
		normalized, err := registry.NormalizeList(explicit)
		if err != nil {
			return nil, fmt.Errorf("无效的镜像加速地址: %w", err)
		}
		explicit = normalized
	}
	mirrors := a.registryMirrors(explicit, false)
	if a.Cfg != nil && a.Cfg.Registry.MaxMirrors > 0 && len(mirrors) > a.Cfg.Registry.MaxMirrors {
		mirrors = mirrors[:a.Cfg.Registry.MaxMirrors]
	}
	return mirrors, nil
}

func pick(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

func printWarnings(warnings []string) {
	for _, w := range warnings {
		logx.Warnf("%s", w)
	}
}
