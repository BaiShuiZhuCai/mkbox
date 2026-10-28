package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/makebox/makebox/internal/backup"
	"github.com/makebox/makebox/internal/logx"
	"github.com/makebox/makebox/internal/system"
)

func (a *App) backupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "备份与恢复 mkbox 管理的配置",
	}
	cmd.AddCommand(a.backupCreateCmd(), a.backupListCmd(), a.backupRestoreCmd())
	return cmd
}

func backupDirFor(a *App) string {
	if !a.OS.IsRoot {
		return filepath.Join(system.UserHome(), ".makebox", "backups")
	}
	return backup.DefaultDir
}

func (a *App) backupCreateCmd() *cobra.Command {
	var (
		output string
		dir    string
		paths  []string
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "创建配置快照（tar.gz）",
		Long:  "默认备份 Docker、containerd、Kubernetes、apt、代理与 mkbox 配置，不包含镜像和 etcd 数据。",
		RunE: func(cmd *cobra.Command, args []string) error {
			useDir := dir
			if useDir == "" {
				useDir = backupDirFor(a)
			}
			if output != "" && !filepath.IsAbs(output) {
				output = filepath.Join(useDir, output)
			}
			res, err := backup.Create(backup.Options{
				Paths:   paths,
				Dir:     useDir,
				Output:  output,
				DryRun:  a.DryRun,
				Verbose: a.Verbose,
			})
			if err != nil {
				return err
			}
			if !a.DryRun {
				logx.Infof("快照路径: %s", res.Archive)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "输出文件（默认 <备份目录>/mkbox-backup-时间.tar.gz）")
	cmd.Flags().StringVar(&dir, "dir", "", "备份目录（默认 /var/backups/makebox 或 ~/.makebox/backups）")
	cmd.Flags().StringArrayVar(&paths, "path", nil, "额外/替代的备份路径，可重复（指定后不再使用默认列表）")
	return cmd
}

func (a *App) backupListCmd() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "列出已有快照",
		RunE: func(cmd *cobra.Command, args []string) error {
			useDir := dir
			if useDir == "" {
				useDir = backupDirFor(a)
			}
			items, err := backup.List(useDir)
			if err != nil {
				return err
			}
			logx.Header("备份目录 %s", useDir)
			if len(items) == 0 {
				logx.Infof("暂无备份，运行 `mkbox backup create` 创建")
				return nil
			}
			for _, item := range items {
				logx.Raw("  %s  %8s  %s", item.ModTime.Format("2006-01-02 15:04:05"), formatSize(item.Size), filepath.Base(item.Path))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "备份目录")
	return cmd
}

func (a *App) backupRestoreCmd() *cobra.Command {
	var (
		dest  string
		force bool
	)
	cmd := &cobra.Command{
		Use:   "restore <快照>",
		Short: "从快照恢复配置（覆盖前会逐文件备份）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			archive := args[0]
			if !filepath.IsAbs(archive) {
				if _, err := os.Stat(archive); err != nil {
					candidate := filepath.Join(backupDirFor(a), archive)
					if _, err := os.Stat(candidate); err == nil {
						archive = candidate
					}
				}
			}
			if st, err := os.Stat(archive); err != nil {
				return fmt.Errorf("快照不存在: %s", archive)
			} else if st.IsDir() {
				return fmt.Errorf("快照必须是普通文件: %s", archive)
			}
			if !filepath.IsAbs(dest) {
				return fmt.Errorf("恢复目标必须是绝对路径: %s", dest)
			}
			if dest == "/" {
				if err := a.requireRoot(); err != nil {
					return err
				}
			}
			prompt := fmt.Sprintf("即将从 %s 恢复到 %s，现有文件会被覆盖（自动备份）", archive, dest)
			if !force {
				prompt += "；建议先执行 mkbox backup create"
			}
			ok, err := a.confirm(prompt + "，继续")
			if err != nil {
				return err
			}
			if !ok {
				logx.Warnf("已取消")
				return nil
			}
			_, err = backup.Restore(backup.RestoreOptions{
				Archive: archive,
				Dest:    dest,
				DryRun:  a.DryRun,
				Verbose: a.Verbose,
			})
			return err
		},
	}
	cmd.Flags().StringVar(&dest, "dest", "/", "恢复目标根目录（测试时可指向临时目录）")
	cmd.Flags().BoolVar(&force, "force", false, "跳过“先创建备份”的提示")
	return cmd
}
