package cli

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/makebox/makebox/internal/logx"
)

func (a *App) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "显示版本与构建信息",
		Run: func(cmd *cobra.Command, args []string) {
			logx.Header("makebox (mkbox)")
			logx.Item("版本", a.Build.Version)
			logx.Item("提交", a.Build.Commit)
			logx.Item("构建时间", a.Build.Date)
			logx.Item("Go", runtime.Version())
			logx.Item("平台", fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH))
			logx.Item("配置", a.CfgPath)
		},
	}
}
