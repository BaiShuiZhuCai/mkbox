package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/makebox/makebox/internal/config"
	"github.com/makebox/makebox/internal/i18n"
	"github.com/makebox/makebox/internal/logx"
)

func (a *App) configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "查看与修改 mkbox 配置",
	}
	cmd.AddCommand(
		a.configShowCmd(),
		a.configGetCmd(),
		a.configSetCmd(),
		a.configPathCmd(),
		a.configInitCmd(),
	)
	return cmd
}

func (a *App) configShowCmd() *cobra.Command {
	var raw bool
	cmd := &cobra.Command{
		Use:   "show",
		Short: "显示完整配置（默认隐藏代理密码）",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.cfgOrErr()
			if err != nil {
				return err
			}
			display := *cfg
			if !raw {
				display.Proxy.HTTP = redactProxy(cfg.Proxy.HTTP)
				display.Proxy.HTTPS = redactProxy(cfg.Proxy.HTTPS)
				display.Network.GHProxy = redactProxy(cfg.Network.GHProxy)
			}
			data, err := json.MarshalIndent(display, "", "  ")
			if err != nil {
				return err
			}
			logx.Header("配置文件 %s", a.CfgPath)
			logx.Raw("%s", string(data))
			if !raw {
				logx.Infof("使用 --raw 显示原始代理密码")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&raw, "raw", false, "显示未脱敏的配置")
	return cmd
}

func (a *App) configGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <键>",
		Short: "读取一个配置项",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.cfgOrErr()
			if err != nil {
				return err
			}
			value, err := configGet(cfg, args[0])
			if err != nil {
				return err
			}
			logx.Raw("%s", value)
			return nil
		},
	}
}

func (a *App) configSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <键> <值>",
		Short: "修改一个配置项",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.cfgOrErr()
			if err != nil {
				return err
			}
			if err := configSet(cfg, args[0], args[1]); err != nil {
				return err
			}
			if err := cfg.Validate(); err != nil {
				return err
			}
			return a.saveConfig(cfg)
		},
	}
}

func (a *App) configPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "显示配置文件路径",
		Run: func(cmd *cobra.Command, args []string) {
			logx.Raw("%s", a.CfgPath)
		},
	}
}

func (a *App) configInitCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "生成默认配置文件",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := os.Stat(a.CfgPath); err == nil && !force {
				return fmt.Errorf("%s 已存在，使用 --force 覆盖（原文件会备份）", a.CfgPath)
			}
			cfg := config.Default()
			if a.DryRun {
				logx.Infof("[dry-run] 生成默认配置 %s", a.CfgPath)
				return nil
			}
			data, err := cfg.Marshal()
			if err != nil {
				return err
			}
			if err := a.FS.EnsureDir(filepath.Dir(a.CfgPath), 0o700); err != nil {
				return fmt.Errorf("创建配置目录: %w", err)
			}
			if err := a.FS.Write(a.CfgPath, data, 0o600); err != nil {
				return err
			}
			if err := os.Chmod(a.CfgPath, 0o600); err != nil {
				return fmt.Errorf("设置配置权限 %s: %w", a.CfgPath, err)
			}
			logx.Successf("已生成默认配置 %s", a.CfgPath)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "覆盖已有配置")
	return cmd
}

func configGet(cfg *config.Config, key string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "version":
		return strconv.Itoa(cfg.Version), nil
	case "language":
		return cfg.Language, nil
	case "registry.mirrors":
		return strings.Join(cfg.Registry.Mirrors, ","), nil
	case "registry.maxmirrors":
		return strconv.Itoa(cfg.Registry.MaxMirrors), nil
	case "registry.timeoutseconds":
		return strconv.Itoa(cfg.Registry.TimeoutSeconds), nil
	case "docker.installmirror":
		return cfg.Docker.InstallMirror, nil
	case "docker.logdriver":
		return cfg.Docker.LogDriver, nil
	case "docker.logmaxsize":
		return cfg.Docker.LogMaxSize, nil
	case "docker.logmaxfile":
		return cfg.Docker.LogMaxFile, nil
	case "docker.dataroot":
		return cfg.Docker.DataRoot, nil
	case "k8s.distro":
		return cfg.K8s.Distro, nil
	case "k8s.version":
		return cfg.K8s.Version, nil
	case "k8s.imagerepository":
		return cfg.K8s.ImageRepository, nil
	case "k8s.repomirror":
		return cfg.K8s.RepoMirror, nil
	case "k8s.podcidr":
		return cfg.K8s.PodCIDR, nil
	case "k8s.servicecidr":
		return cfg.K8s.ServiceCIDR, nil
	case "k8s.cni":
		return cfg.K8s.CNI, nil
	case "proxy.http":
		return cfg.Proxy.HTTP, nil
	case "proxy.https":
		return cfg.Proxy.HTTPS, nil
	case "proxy.noproxy":
		return strings.Join(cfg.Proxy.NoProxy, ","), nil
	case "mirror.os":
		return cfg.Mirror.OS, nil
	case "network.ghproxy":
		return cfg.Network.GHProxy, nil
	case "network.checktimeoutms":
		return strconv.Itoa(cfg.Network.CheckTimeoutMS), nil
	default:
		return "", fmt.Errorf("未知配置项 %q；可用项: %s", key, strings.Join(configKeys(), ", "))
	}
}

func configSet(cfg *config.Config, key, value string) error {
	value = strings.TrimSpace(value)
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "version":
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			return fmt.Errorf("version 必须是正整数")
		}
		cfg.Version = n
	case "language":
		code, ok := i18n.Normalize(value)
		if !ok {
			return fmt.Errorf("language 必须是 zh-CN 或 en")
		}
		cfg.Language = code
	case "registry.mirrors":
		if value == "" {
			cfg.Registry.Mirrors = []string{}
		} else {
			parts := strings.Split(value, ",")
			out := make([]string, 0, len(parts))
			for _, p := range parts {
				if p = strings.TrimSpace(p); p != "" {
					out = append(out, p)
				}
			}
			cfg.Registry.Mirrors = out
		}
	case "registry.maxmirrors":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("registry.maxMirrors 必须是整数")
		}
		cfg.Registry.MaxMirrors = n
	case "registry.timeoutseconds":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("registry.timeoutSeconds 必须是整数")
		}
		cfg.Registry.TimeoutSeconds = n
	case "docker.installmirror":
		cfg.Docker.InstallMirror = value
	case "docker.logdriver":
		cfg.Docker.LogDriver = value
	case "docker.logmaxsize":
		cfg.Docker.LogMaxSize = value
	case "docker.logmaxfile":
		cfg.Docker.LogMaxFile = value
	case "docker.dataroot":
		cfg.Docker.DataRoot = value
	case "k8s.distro":
		cfg.K8s.Distro = value
	case "k8s.version":
		cfg.K8s.Version = value
	case "k8s.imagerepository":
		cfg.K8s.ImageRepository = value
	case "k8s.repomirror":
		cfg.K8s.RepoMirror = value
	case "k8s.podcidr":
		cfg.K8s.PodCIDR = value
	case "k8s.servicecidr":
		cfg.K8s.ServiceCIDR = value
	case "k8s.cni":
		cfg.K8s.CNI = value
	case "proxy.http":
		cfg.Proxy.HTTP = value
	case "proxy.https":
		cfg.Proxy.HTTPS = value
	case "proxy.noproxy":
		if value == "" {
			cfg.Proxy.NoProxy = nil
		} else {
			parts := strings.Split(value, ",")
			out := make([]string, 0, len(parts))
			for _, p := range parts {
				if p = strings.TrimSpace(p); p != "" {
					out = append(out, p)
				}
			}
			cfg.Proxy.NoProxy = out
		}
	case "mirror.os":
		cfg.Mirror.OS = value
	case "network.ghproxy":
		cfg.Network.GHProxy = value
	case "network.checktimeoutms":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("network.checkTimeoutMs 必须是整数")
		}
		cfg.Network.CheckTimeoutMS = n
	default:
		return fmt.Errorf("未知配置项 %q；可用项: %s", key, strings.Join(configKeys(), ", "))
	}
	return nil
}

func configKeys() []string {
	keys := []string{
		"version", "language",
		"registry.mirrors", "registry.maxMirrors", "registry.timeoutSeconds",
		"docker.installMirror", "docker.logDriver", "docker.logMaxSize", "docker.logMaxFile", "docker.dataRoot",
		"k8s.distro", "k8s.version", "k8s.imageRepository", "k8s.repoMirror",
		"k8s.podCIDR", "k8s.serviceCIDR", "k8s.cni",
		"proxy.http", "proxy.https", "proxy.noProxy",
		"mirror.os", "network.ghProxy", "network.checkTimeoutMs",
	}
	sort.Strings(keys)
	return keys
}
