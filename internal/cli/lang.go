package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/makebox/makebox/internal/config"
	"github.com/makebox/makebox/internal/i18n"
)

// resolveLanguage determines the interface language before the command tree is
// built, so that help text can already be localized.
//
// Precedence: --lang > MKBOX_LANG > config file language > zh-CN.
func resolveLanguage(args []string) (string, error) {
	if raw, ok := scanFlag(args, "lang"); ok {
		code, valid := i18n.Normalize(raw)
		if !valid {
			return "", fmt.Errorf("不支持的界面语言 %q（可选 zh-CN、en）", raw)
		}
		return code, nil
	}
	if raw := strings.TrimSpace(os.Getenv("MKBOX_LANG")); raw != "" {
		code, valid := i18n.Normalize(raw)
		if !valid {
			return "", fmt.Errorf("MKBOX_LANG 不支持 %q（可选 zh-CN、en）", raw)
		}
		return code, nil
	}
	path := config.Path()
	if p, ok := scanFlag(args, "config"); ok && p != "" {
		path = p
	}
	cfg, err := config.Load(path)
	if err != nil {
		return i18n.Chinese, nil
	}
	code, valid := i18n.Normalize(cfg.Language)
	if !valid {
		return i18n.Chinese, nil
	}
	return code, nil
}

// scanFlag finds a string flag before cobra parses the arguments.
func scanFlag(args []string, name string) (string, bool) {
	long := "--" + name
	var value string
	found := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		switch {
		case arg == long && i+1 < len(args):
			value = args[i+1]
			found = true
			i++
		case strings.HasPrefix(arg, long+"="):
			value = strings.TrimPrefix(arg, long+"=")
			found = true
		}
	}
	return value, found
}

// localizeCommands translates cobra metadata in place.
func localizeCommands(cmd *cobra.Command) {
	cmd.Use = i18n.T(cmd.Use)
	cmd.Short = i18n.T(cmd.Short)
	if cmd.Long != "" {
		cmd.Long = i18n.T(cmd.Long)
	}
	if cmd.Example != "" {
		cmd.Example = i18n.T(cmd.Example)
	}
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Usage != "" {
			f.Usage = i18n.T(f.Usage)
		}
	})
	for _, group := range cmd.Groups() {
		group.Title = i18n.T(group.Title)
	}
	for _, child := range cmd.Commands() {
		localizeCommands(child)
	}
}
