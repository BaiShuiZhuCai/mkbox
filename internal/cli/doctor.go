package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/makebox/makebox/internal/doctor"
	"github.com/makebox/makebox/internal/execx"
	"github.com/makebox/makebox/internal/i18n"
	"github.com/makebox/makebox/internal/logx"
)

func (a *App) doctorCmd() *cobra.Command {
	var (
		withNet bool
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "检查系统是否满足 Docker / Kubernetes 安装条件",
		Long:  "只读体检：发行版、内核、资源、内核模块、防火墙、Docker、containerd、Kubernetes、配置与可选的网络连通性。",
		RunE: func(cmd *cobra.Command, args []string) error {
			r := execx.New(false, a.Verbose)
			if timeout <= 0 {
				timeout = 5 * time.Second
				if a.Cfg != nil && a.Cfg.Network.CheckTimeoutMS > 0 {
					timeout = time.Duration(a.Cfg.Network.CheckTimeoutMS) * time.Millisecond
				}
			}
			rep := doctor.Run(a.OS, r, doctor.Options{Net: withNet, Timeout: timeout, ConfigPath: a.CfgPath})
			logx.Header("mkbox 环境体检")
			for _, c := range rep.Checks {
				name, detail := i18n.Translate(c.Name), i18n.Translate(c.Detail)
				switch c.Level {
				case doctor.OK:
					logx.Successf("%s: %s", name, detail)
				case doctor.Warn:
					logx.Warnf("%s: %s", name, detail)
				case doctor.Fail:
					logx.Errorf("%s: %s", name, detail)
				default:
					logx.Infof("%s: %s", name, detail)
				}
				if c.Hint != "" {
					logx.Raw("    ↳ %s", i18n.Translate(c.Hint))
				}
			}
			counts := rep.Counts()
			logx.Header("总结")
			logx.Raw("  %s %d 项通过   %s %d 项警告   %s %d 项失败   %s %d 项提示",
				"✓", counts[doctor.OK], "!", counts[doctor.Warn], "✗", counts[doctor.Fail], "·", counts[doctor.Info])
			if counts[doctor.Fail] > 0 {
				return fmt.Errorf("体检发现 %d 项严重问题", counts[doctor.Fail])
			}
			if counts[doctor.Warn] > 0 {
				logx.Infof("警告项不一定会阻止安装，可按提示处理")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&withNet, "net", false, "同时测试外网和镜像加速器连通性")
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "网络检查超时（默认取 network.checkTimeoutMs 或 5s）")
	return cmd
}
