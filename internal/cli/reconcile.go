package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/makebox/makebox/internal/coexist"
	"github.com/makebox/makebox/internal/containerd"
	"github.com/makebox/makebox/internal/docker"
	"github.com/makebox/makebox/internal/k8s"
	"github.com/makebox/makebox/internal/logx"
	"github.com/makebox/makebox/internal/reconcile"
)

// ExitError carries a process exit code for commands such as diff --exit-code.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

// ExitCode returns the intended process exit code for an error.
func ExitCode(err error) int {
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	if err != nil {
		return 1
	}
	return 0
}

func (a *App) planCmd() *cobra.Command {
	var (
		asJSON        bool
		skipPreflight bool
	)
	cmd := &cobra.Command{
		Use:   "plan [docker|k8s|all]",
		Short: "只读比较期望状态与主机现状",
		Long: `根据配置计算需要执行的动作和文件差异，不会修改主机。
scope 默认为 all；可用配置文件调整镜像加速、Kubernetes 版本等期望状态。`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			scope := ""
			if len(args) > 0 {
				scope = args[0]
			}
			p, err := a.buildPlan(scope, skipPreflight)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(redactPlan(p))
			}
			a.printPlan(p, true)
			if p.Empty() {
				return nil
			}
			logx.Infof("运行 `sudo mkbox apply %s` 应用上述计划", p.Scope)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "输出机器可读 JSON")
	cmd.Flags().BoolVar(&skipPreflight, "skip-preflight", false, "计划时不包含 Kubernetes 预检")
	return cmd
}

func (a *App) diffCmd() *cobra.Command {
	var (
		asJSON   bool
		exitCode bool
	)
	cmd := &cobra.Command{
		Use:   "diff [docker|k8s|all]",
		Short: "只显示受管文件的差异",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			scope := ""
			if len(args) > 0 {
				scope = args[0]
			}
			p, err := a.buildPlan(scope, false)
			if err != nil {
				return err
			}
			if asJSON {
				if err := printJSON(redactPlan(p).Diffs); err != nil {
					return err
				}
			} else if len(p.Diffs) == 0 {
				logx.Successf("受管文件已收敛，无差异")
			} else {
				for _, d := range p.Diffs {
					logx.Header("%s", d.Path)
					if d.Note != "" {
						logx.Infof("%s", d.Note)
					}
					logx.Raw("%s", strings.TrimRight(d.Unified, "\n"))
				}
			}
			if exitCode && len(p.Diffs) > 0 {
				return &ExitError{Code: 2, Err: fmt.Errorf("存在 %d 个文件差异", len(p.Diffs))}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "输出机器可读 JSON")
	cmd.Flags().BoolVar(&exitCode, "exit-code", false, "存在差异时以退出码 2 结束")
	return cmd
}

func (a *App) applyCmd() *cobra.Command {
	var (
		autoRollback  bool
		stateDir      string
		asJSON        bool
		skipPreflight bool
		force         bool
	)
	cmd := &cobra.Command{
		Use:   "apply [docker|k8s|all]",
		Short: "按计划收敛主机状态（带事务日志与回滚）",
		Long: `执行 plan 计算出的动作。每个动作执行前会快照受管文件并写入
/var/lib/mkbox/journals，失败时可用 mkbox rollback 恢复。`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireRoot(); err != nil {
				return err
			}
			scope := ""
			if len(args) > 0 {
				scope = args[0]
			}
			p, err := a.buildPlan(scope, skipPreflight)
			if err != nil {
				return err
			}
			if p.Empty() {
				logx.Successf("主机已收敛，无需修改")
				if asJSON {
					return printJSON(reconcile.ApplyResult{})
				}
				return nil
			}
			a.printPlan(p, false)
			if a.DryRun {
				logx.Warnf("[dry-run] 不会执行动作，也不会写入 journal")
				return nil
			}
			prompt := fmt.Sprintf("即将应用 %d 个动作", len(p.Actions))
			if n := len(p.IrreversibleActions()); n > 0 {
				prompt += fmt.Sprintf("，其中 %d 个不可自动回滚", n)
				if !force {
					logx.Warnf("不可回滚动作: %s", strings.Join(p.IrreversibleActions(), "；"))
					return fmt.Errorf("计划包含不可逆动作，已停止；确认后请添加 --force")
				}
			}
			ok, err := a.confirm(prompt + "，继续")
			if err != nil {
				return err
			}
			if !ok {
				logx.Warnf("已取消")
				return nil
			}
			store := reconcile.NewJournalStore(stateDir)
			services := reconcile.ServiceStates(a.R, reconcile.PlanServices(p))
			res, err := reconcile.Apply(p, &reconcileExecutor{app: a, profile: p.Profile}, reconcile.ApplyOptions{
				Store:        store,
				AutoRollback: autoRollback,
				Services:     services,
				Restart: func(svcs []string) error {
					return reconcile.RestartSystemd(a.R, svcs)
				},
				Out: func(format string, args ...any) { logx.Infof(format, args...) },
			})
			if asJSON {
				if jsonErr := printJSON(res); jsonErr != nil && err == nil {
					err = jsonErr
				}
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&autoRollback, "auto-rollback", false, "任一步骤失败时自动回滚已完成动作")
	cmd.Flags().StringVar(&stateDir, "state-dir", "", "journal 根目录（默认 /var/lib/mkbox，可用 MKBOX_STATE_DIR）")
	cmd.Flags().BoolVar(&asJSON, "json", false, "输出机器可读 JSON 结果")
	cmd.Flags().BoolVar(&skipPreflight, "skip-preflight", false, "不包含 Kubernetes 预检动作")
	cmd.Flags().BoolVar(&force, "force", false, "允许执行计划中的不可逆动作")
	return cmd
}

func (a *App) rollbackCmd() *cobra.Command {
	var (
		stateDir string
		force    bool
		asJSON   bool
	)
	cmd := &cobra.Command{
		Use:   "rollback [journal-id]",
		Short: "回滚一次 apply 事务",
		Long: `恢复 journal 中快照的受管文件，并重启事务前处于运行状态的服务。
软件包安装、集群初始化等不可逆动作需要 --force 才会继续（仍不会自动卸载/重置集群）。`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireRoot(); err != nil {
				return err
			}
			store := reconcile.NewJournalStore(stateDir)
			var j *reconcile.Journal
			var err error
			if len(args) == 0 {
				j, err = store.Latest()
			} else {
				j, err = store.Load(args[0])
			}
			if err != nil {
				return err
			}
			ok, err := a.confirm(fmt.Sprintf("即将回滚 journal %s（%d 个文件快照），继续", j.ID, len(j.Files)))
			if err != nil {
				return err
			}
			if !ok {
				logx.Warnf("已取消")
				return nil
			}
			_, res, err := store.Rollback(j.ID, reconcile.RollbackOptions{
				Force:  force,
				DryRun: a.DryRun,
				Restart: func(svcs []string) error {
					return reconcile.RestartSystemd(a.R, svcs)
				},
				Out: func(format string, args ...any) { logx.Infof(format, args...) },
			})
			if asJSON && res != nil {
				if jsonErr := printJSON(res); jsonErr != nil && err == nil {
					err = jsonErr
				}
			}
			if err != nil {
				return err
			}
			if res != nil && len(res.Warnings) > 0 {
				for _, w := range res.Warnings {
					logx.Warnf("%s", w)
				}
			}
			if a.DryRun {
				logx.Warnf("[dry-run] 未修改文件")
			} else {
				logx.Successf("回滚完成: journal %s", j.ID)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&stateDir, "state-dir", "", "journal 根目录（默认 /var/lib/mkbox）")
	cmd.Flags().BoolVar(&force, "force", false, "确认继续处理不可逆动作（不会卸载软件包）")
	cmd.Flags().BoolVar(&asJSON, "json", false, "输出机器可读 JSON 结果")
	return cmd
}

func (a *App) buildPlan(scopeRaw string, skipPreflight bool) (reconcile.Plan, error) {
	cfg, err := a.cfgOrErr()
	if err != nil {
		return reconcile.Plan{}, err
	}
	scope, err := reconcile.ParseScope(scopeRaw)
	if err != nil {
		return reconcile.Plan{}, err
	}
	profile := reconcile.ProfileFromConfig(cfg, scope)
	if skipPreflight {
		profile.K8s.SkipPreflight = true
	}
	obs := reconcile.Observe(a.R, a.OS)
	p, err := reconcile.BuildPlan(profile, obs, a.OS, reconcile.PlanOptions{SkipPreflight: skipPreflight})
	if err != nil {
		return reconcile.Plan{}, err
	}
	p.Host.GeneratedBy = "mkbox " + a.Build.Version
	return p, nil
}

func (a *App) printPlan(p reconcile.Plan, diffs bool) {
	logx.Header("收敛计划（%s）", p.Scope)
	logx.Item("主机", p.Host.Hostname)
	logx.Item("系统", p.Host.OS+" / "+p.Host.Arch)
	if p.Empty() {
		logx.Successf("没有需要执行的动作")
		return
	}
	for i, action := range p.Actions {
		reversible := "可回滚"
		if !action.Reversible {
			reversible = "不可回滚"
		}
		logx.Raw("  [%d] %-18s %s（风险 %s，%s）", i+1, action.ID, action.Title, action.Risk, reversible)
		if action.Reason != "" {
			logx.Raw("      %s", action.Reason)
		}
		for _, d := range action.Details {
			logx.Raw("      - %s", d)
		}
	}
	for _, w := range p.Warnings {
		logx.Warnf("%s", w)
	}
	if diffs && len(p.Diffs) > 0 {
		logx.Header("文件差异")
		for _, d := range p.Diffs {
			if d.Note != "" {
				logx.Infof("%s（%s）", d.Path, d.Note)
			} else {
				logx.Infof("%s", d.Path)
			}
			logx.Raw("%s", strings.TrimRight(d.Unified, "\n"))
		}
	}
}

func redactPlan(p reconcile.Plan) reconcile.Plan {
	p.Profile.Proxy.URL = redactProxy(p.Profile.Proxy.URL)
	p.Profile.K8s.GHProxy = redactProxy(p.Profile.K8s.GHProxy)
	return p
}

func printJSON(v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, string(data))
	return nil
}

// reconcileExecutor maps plan actions onto mkbox's existing imperative code.
type reconcileExecutor struct {
	app     *App
	profile reconcile.Profile
}

func (e *reconcileExecutor) Execute(action reconcile.Action) error {
	a := e.app
	p := e.profile
	switch action.ID {
	case "docker.install":
		user := ""
		if a.OS.SudoUser != nil {
			user = a.OS.SudoUser.Name
		}
		return docker.NewEngine(a.OS, a.R, a.FS, p.Docker.InstallMirror, user).Install()
	case "docker.start":
		return docker.NewEngine(a.OS, a.R, a.FS, p.Docker.InstallMirror, "").Enable()
	case "docker.configure":
		mgr := docker.NewManager(a.R, a.FS, a.OS)
		dc, err := mgr.Load()
		if err != nil {
			return err
		}
		if err := dc.SetMirrors(p.Docker.Mirrors); err != nil {
			return err
		}
		dc.SetLogging(p.Docker.LogDriver, p.Docker.LogMaxSize, p.Docker.LogMaxFile)
		dc.SetDataRoot(p.Docker.DataRoot)
		if err := mgr.Save(dc); err != nil {
			return err
		}
		return mgr.Restart()
	case "docker.proxy.set":
		return docker.NewManager(a.R, a.FS, a.OS).SetProxy(p.Proxy.URL, p.Proxy.NoProxy)
	case "docker.proxy.clear":
		return docker.NewManager(a.R, a.FS, a.OS).ClearProxy()
	case "containerd.configure":
		mgr := containerd.NewManager(a.R, a.FS, a.OS)
		if len(p.Docker.Mirrors) == 0 {
			if err := a.FS.Remove(containerd.CertsDir + "/docker.io/hosts.toml"); err != nil {
				return err
			}
		}
		if err := mgr.Configure(p.Docker.Mirrors, p.K8s.ImageRepository); err != nil {
			return err
		}
		return mgr.Restart()
	case "containerd.proxy.set":
		return containerd.NewManager(a.R, a.FS, a.OS).SetProxy(p.Proxy.URL, p.Proxy.NoProxy)
	case "containerd.proxy.clear":
		return containerd.NewManager(a.R, a.FS, a.OS).ClearProxy()
	case "k8s.install":
		inst := &k8s.Installer{
			OS:              a.OS,
			R:               a.R,
			FS:              a.FS,
			Distro:          p.K8s.Distro,
			Version:         p.K8s.Version,
			RepoMirror:      p.K8s.RepoMirror,
			Mirrors:         p.Docker.Mirrors,
			ImageRepository: p.K8s.ImageRepository,
		}
		return inst.Run()
	case "k8s.preflight":
		pf := &k8s.Preflight{
			OS:              a.OS,
			R:               a.R,
			FS:              a.FS,
			Distro:          p.K8s.Distro,
			CNI:             p.K8s.CNI,
			Mirrors:         p.Docker.Mirrors,
			ImageRepository: p.K8s.ImageRepository,
			SkipFirewall:    p.K8s.SkipFirewall,
		}
		return pf.Run()
	case "k8s.init":
		in := &k8s.Initializer{OS: a.OS, R: a.R, FS: a.FS, Opts: reconcile.KubeadmInitOptions(p)}
		res, err := in.Run()
		if err != nil {
			return err
		}
		logx.Header("初始化结果")
		if res.NodeIP != "" {
			logx.Item("节点 IP", res.NodeIP)
		}
		if res.Kubeconfig != "" {
			logx.Item("kubeconfig", res.Kubeconfig)
		}
		printWarnings(res.Warnings)
		if res.JoinCommand != "" {
			logx.Header("加入集群命令")
			logx.Raw("%s", res.JoinCommand)
		}
		return nil
	case "k8s.start":
		if !a.OS.HasSystemd() {
			return fmt.Errorf("未检测到 systemd，请手动启动 Kubernetes 节点服务")
		}
		service := "kubelet"
		if p.K8s.Distro == "k3s" {
			service = "k3s"
		}
		return a.R.Run("systemctl", "enable", "--now", service)
	}
	return fmt.Errorf("未知动作 %q", action.ID)
}

func (a *App) runtimeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "runtime",
		Short: "检测 Docker/containerd/Kubernetes 运行时共存关系",
	}
	cmd.AddCommand(a.runtimeDetectCmd(), a.runtimePlanCmd())
	return cmd
}

func (a *App) runtimeDetectCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "detect",
		Short: "只读输出运行时事实",
		RunE: func(cmd *cobra.Command, args []string) error {
			f := coexist.Detect(a.R, a.OS)
			if asJSON {
				f.HTTPProxy = redactProxy(f.HTTPProxy)
				f.HTTPSProxy = redactProxy(f.HTTPSProxy)
				return printJSON(f)
			}
			logx.Header("运行时事实")
			logx.Item("cgroup", fmt.Sprintf("v%d", f.CgroupVersion))
			logx.Item("Docker", fmt.Sprintf("installed=%v running=%v driver=%s", f.DockerInstalled, f.DockerRunning, f.DockerCgroupDriver))
			logx.Item("containerd", fmt.Sprintf("installed=%v running=%v systemdCgroup=%v", f.ContainerdInstalled, f.ContainerdRunning, f.ContainerdSystemdCgroup))
			logx.Item("cri-dockerd", fmt.Sprintf("installed=%v running=%v", f.CriDockerdInstalled, f.CriDockerdRunning))
			logx.Item("kubelet", fmt.Sprintf("installed=%v running=%v", f.KubeletInstalled, f.KubeletRunning))
			logx.Item("k3s", fmt.Sprintf("installed=%v running=%v", f.K3sInstalled, f.K3sRunning))
			logx.Item("swap", fmt.Sprintf("%v", f.SwapEnabled))
			logx.Item("iptables", f.IptablesBackend)
			logx.Item("occupied ports", coexist.FormatPorts(f.Ports))
			if len(f.CRISockets) > 0 {
				logx.Item("CRI sockets", strings.Join(f.CRISockets, ", "))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "输出机器可读 JSON")
	return cmd
}

func (a *App) runtimePlanCmd() *cobra.Command {
	var (
		asJSON bool
		strict bool
	)
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "给出运行时共存拓扑与整改建议",
		RunE: func(cmd *cobra.Command, args []string) error {
			f := coexist.Detect(a.R, a.OS)
			p := coexist.BuildPlan(f)
			if asJSON {
				p.Facts.HTTPProxy = redactProxy(p.Facts.HTTPProxy)
				p.Facts.HTTPSProxy = redactProxy(p.Facts.HTTPSProxy)
				if err := printJSON(p); err != nil {
					return err
				}
			} else {
				logx.Header("运行时拓扑")
				logx.Item("拓扑", p.Topology)
				logx.Raw("%s", p.Summary)
				for _, finding := range p.Findings {
					line := fmt.Sprintf("[%s] %s: %s", finding.Severity, finding.Code, finding.Message)
					logx.Raw("%s", line)
					if finding.Action != "" {
						logx.Raw("    -> %s", finding.Action)
					}
				}
				if len(p.NextSteps) > 0 {
					logx.Header("建议步骤")
					for i, step := range p.NextSteps {
						logx.Raw("  %d. %s", i+1, step)
					}
				}
			}
			if strict && p.HasErrors() {
				return fmt.Errorf("运行时存在阻塞性问题，请先按建议修复")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "输出机器可读 JSON")
	cmd.Flags().BoolVar(&strict, "strict", false, "存在 error 级发现时返回失败")
	return cmd
}

// reportData is the evidence bundle emitted by `mkbox report`.
type reportData struct {
	GeneratedAt string                     `json:"generatedAt"`
	Version     string                     `json:"version"`
	Host        reconcile.HostInfo         `json:"host"`
	Plan        reconcile.Plan             `json:"plan"`
	Runtime     coexist.Plan               `json:"runtime"`
	Journals    []reconcile.JournalSummary `json:"journals"`
}

func (a *App) reportCmd() *cobra.Command {
	var (
		asJSON   bool
		output   string
		stateDir string
	)
	cmd := &cobra.Command{
		Use:   "report",
		Short: "生成可归档的环境证据 JSON（计划+运行时+journal）",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.buildPlan("all", false)
			if err != nil {
				return err
			}
			p = redactPlan(p)
			f := coexist.Detect(a.R, a.OS)
			rp := coexist.BuildPlan(f)
			rp.Facts.HTTPProxy = redactProxy(rp.Facts.HTTPProxy)
			rp.Facts.HTTPSProxy = redactProxy(rp.Facts.HTTPSProxy)
			journals, err := reconcile.NewJournalStore(stateDir).List()
			if err != nil {
				return err
			}
			rep := reportData{
				GeneratedAt: time.Now().UTC().Format(time.RFC3339),
				Version:     a.Build.Version,
				Host:        p.Host,
				Plan:        p,
				Runtime:     rp,
				Journals:    journals,
			}
			if output != "" {
				data, err := json.MarshalIndent(rep, "", "  ")
				if err != nil {
					return err
				}
				if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(output, append(data, '\n'), 0o600); err != nil {
					return fmt.Errorf("写入报告: %w", err)
				}
				logx.Successf("报告已写入 %s", output)
			}
			if asJSON || output == "" {
				return printJSON(rep)
			}
			logx.Infof("计划动作 %d 个，运行时发现 %d 个，journal %d 条", len(p.Actions), len(rp.Findings), len(journals))
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "输出 JSON")
	cmd.Flags().StringVarP(&output, "output", "o", "", "写入 JSON 文件（0600）")
	cmd.Flags().StringVar(&stateDir, "state-dir", "", "journal 根目录（默认 /var/lib/mkbox）")
	return cmd
}
