package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func (a *App) completionCmd() *cobra.Command {
	return &cobra.Command{
		Use:                   "completion [bash|zsh|fish|powershell]",
		Short:                 "生成 shell 自动补全脚本",
		Long:                  "生成 mkbox 的 shell 自动补全脚本，并输出到标准输出。",
		DisableFlagsInUseLine: true,
		ValidArgs:             []string{"bash", "zsh", "fish", "powershell"},
		Args:                  cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		Example: `  # bash
  source <(mkbox completion bash)

  # zsh
  mkbox completion zsh > "${fpath[1]}/_mkbox"

  # fish
  mkbox completion fish | source

  # PowerShell
  mkbox completion powershell | Out-String | Invoke-Expression`,
		RunE: func(cmd *cobra.Command, args []string) error {
			root := cmd.Root()
			switch args[0] {
			case "bash":
				return root.GenBashCompletionV2(os.Stdout, true)
			case "zsh":
				return root.GenZshCompletion(os.Stdout)
			case "fish":
				return root.GenFishCompletion(os.Stdout, true)
			case "powershell":
				return root.GenPowerShellCompletionWithDesc(os.Stdout)
			default:
				return fmt.Errorf("不支持的 shell: %s", args[0])
			}
		},
	}
}
