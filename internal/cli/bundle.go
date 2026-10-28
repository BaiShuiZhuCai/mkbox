package cli

import (
	"crypto/ed25519"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/makebox/makebox/internal/bundle"
	"github.com/makebox/makebox/internal/logx"
)

func (a *App) bundleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bundle",
		Short: "构建、签名、校验并分发离线 bundle（OCI artifact）",
	}
	cmd.AddCommand(
		a.bundleBuildCmd(),
		a.bundleVerifyCmd(),
		a.bundleInspectCmd(),
		a.bundleKeygenCmd(),
		a.bundlePushCmd(),
		a.bundlePullCmd(),
	)
	return cmd
}

func parsePlatform(raw string) (bundle.Platform, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return bundle.Platform{}, nil
	}
	parts := strings.Split(raw, "/")
	if len(parts) < 2 || len(parts) > 3 || parts[0] == "" || parts[1] == "" {
		return bundle.Platform{}, fmt.Errorf("--platform 必须形如 linux/amd64 或 linux/arm/v7")
	}
	p := bundle.Platform{OS: parts[0], Arch: parts[1]}
	if len(parts) == 3 {
		p.Variant = parts[2]
	}
	return p, nil
}

func parseCreatedAt(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("--created-at 必须是 RFC3339 时间: %w", err)
	}
	return t, nil
}

func (a *App) bundleBuildCmd() *cobra.Command {
	var (
		output    string
		name      string
		version   string
		platform  string
		license   string
		source    string
		commit    string
		createdAt string
		keyPath   string
		files     []string
		asJSON    bool
	)
	cmd := &cobra.Command{
		Use:   "build",
		Short: "构建确定性 tar.gz bundle（可选 Ed25519 签名）",
		Long: `将若干文件打包为可复现的 tar.gz：条目排序、固定属主与时间戳，
包含 manifest.json、checksums.txt 和 SPDX SBOM。使用 --key 时对 manifest 签名。
设置 SOURCE_DATE_EPOCH 或 --created-at 可让多次构建字节完全一致。`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(files) == 0 {
				return fmt.Errorf("至少需要一个 --file")
			}
			if output == "" {
				output = bundle.ArchiveName(nameOr(name), versionOr(version))
			}
			if st, err := os.Stat(output); err == nil && st.IsDir() {
				output = filepath.Join(output, bundle.ArchiveName(nameOr(name), versionOr(version)))
			}
			created, err := parseCreatedAt(createdAt)
			if err != nil {
				return err
			}
			plat, err := parsePlatform(platform)
			if err != nil {
				return err
			}
			var key ed25519.PrivateKey
			if keyPath != "" {
				key, err = bundle.LoadPrivateKey(keyPath)
				if err != nil {
					return err
				}
			}
			inputs := make([]bundle.Input, 0, len(files))
			for _, f := range files {
				inputs = append(inputs, bundle.Input{Path: f})
			}
			res, err := bundle.Build(bundle.BuildOptions{
				OutPath:    output,
				Name:       name,
				Version:    version,
				CreatedAt:  created,
				Platform:   plat,
				License:    license,
				Source:     source,
				Commit:     commit,
				Inputs:     inputs,
				SigningKey: key,
			})
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(res)
			}
			logx.Successf("bundle 已写入 %s（%s）", res.Path, formatSize(res.Size))
			logx.Item("sha256", strings.TrimPrefix(res.Digest, "sha256:"))
			logx.Item("文件数", fmt.Sprintf("%d", res.Files))
			if res.Signed {
				logx.Item("签名", "已签名，keyId "+res.KeyID)
			} else {
				logx.Warnf("未签名；生产环境建议使用 --key 签名")
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVarP(&output, "output", "o", "", "输出文件（默认 <name>-<version>.mkbox.tar.gz）")
	fl.StringVar(&name, "name", "", "bundle 名称")
	fl.StringVar(&version, "version", "", "bundle 版本")
	fl.StringVar(&platform, "platform", "", "目标平台，如 linux/amd64")
	fl.StringVar(&license, "license", "", "SPDX 许可证标识，如 MIT")
	fl.StringVar(&source, "source", "", "源码地址（写入 provenance）")
	fl.StringVar(&commit, "commit", "", "源码提交（写入 provenance）")
	fl.StringVar(&createdAt, "created-at", "", "固定构建时间（RFC3339），用于可复现构建")
	fl.StringVar(&keyPath, "key", "", "Ed25519 私钥 PEM（签名）")
	fl.StringArrayVar(&files, "file", nil, "要打包的文件，可重复")
	fl.BoolVar(&asJSON, "json", false, "输出机器可读 JSON")
	return cmd
}

func shortDigest(d string) string {
	if len(d) <= 19 {
		return d
	}
	return d[:19] + "…"
}

func nameOr(s string) string {
	if s == "" {
		return "bundle"
	}
	return s
}

func versionOr(s string) string {
	if s == "" {
		return "0.0.0"
	}
	return s
}

func (a *App) bundleVerifyCmd() *cobra.Command {
	var (
		keyPath       string
		require       bool
		allowUnsigned bool
		asJSON        bool
	)
	cmd := &cobra.Command{
		Use:   "verify <bundle.tar.gz>",
		Short: "校验摘要、checksums 与 Ed25519 签名",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := bundle.VerifyOptions{RequireSignature: require, AllowUnsigned: allowUnsigned}
			if keyPath != "" {
				key, err := bundle.LoadPublicKey(keyPath)
				if err != nil {
					return err
				}
				opts.TrustedKey = key
			}
			res, err := bundle.VerifyFile(args[0], opts)
			if asJSON && res != nil {
				_ = printJSON(res)
			}
			if err != nil {
				return err
			}
			if !asJSON {
				logx.Successf("bundle 校验通过")
				logx.Item("名称", res.Manifest.Name+" "+res.Manifest.Version)
				logx.Item("文件", fmt.Sprintf("%d 个（%s）", res.Files, formatSize(res.Size)))
				if res.Signed {
					logx.Item("签名", "有效，keyId "+res.KeyID)
				} else {
					logx.Warnf("bundle 未签名")
				}
				if res.Trusted {
					logx.Item("可信公钥", "匹配")
				}
				for _, w := range res.Warnings {
					logx.Warnf("%s", w)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&keyPath, "key", "", "可信的 Ed25519 公钥 PEM")
	cmd.Flags().BoolVar(&require, "require-signature", false, "要求 bundle 必须已签名（默认即要求）")
	cmd.Flags().BoolVar(&allowUnsigned, "allow-unsigned", false, "接受未签名 bundle（仅用于本地构建产物）")
	cmd.Flags().BoolVar(&asJSON, "json", false, "输出机器可读 JSON")
	return cmd
}

func (a *App) bundleInspectCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "inspect <bundle.tar.gz>",
		Short: "查看 bundle 清单、SBOM 与签名信息",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := bundle.Inspect(args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(res)
			}
			m := res.Manifest
			logx.Header("Bundle")
			logx.Item("名称", m.Name+" "+m.Version)
			logx.Item("创建时间", m.CreatedAt)
			logx.Item("平台", strings.Trim(strings.Join([]string{m.Platform.OS, m.Platform.Arch, m.Platform.Variant}, "/"), "/"))
			logx.Item("构建器", m.Builder)
			logx.Item("大小", formatSize(res.Size))
			for _, f := range m.Files {
				logx.Raw("  %-40s %10s  %s", f.Name, formatSize(f.Size), shortDigest(f.Digest))
			}
			if res.Signature != nil {
				logx.Item("签名", "Ed25519 keyId "+res.Signature.KeyID)
			} else {
				logx.Item("签名", "无")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "输出机器可读 JSON")
	return cmd
}

func (a *App) bundleKeygenCmd() *cobra.Command {
	var (
		dir    string
		force  bool
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "keygen",
		Short: "生成 Ed25519 签名密钥（私钥 0600）",
		RunE: func(cmd *cobra.Command, args []string) error {
			priv, pub, keyID, err := bundle.GenerateKeyPair(dir, force)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(map[string]string{
					"privateKey": priv,
					"publicKey":  pub,
					"keyId":      keyID,
				})
			}
			logx.Successf("密钥已生成")
			logx.Item("私钥", priv)
			logx.Item("公钥", pub)
			logx.Item("keyId", keyID)
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "out-dir", ".", "输出目录")
	cmd.Flags().BoolVar(&force, "force", false, "覆盖已存在的密钥")
	cmd.Flags().BoolVar(&asJSON, "json", false, "输出机器可读 JSON")
	return cmd
}

func registryCredentials(user string, passwordStdin bool) (string, string, error) {
	if user == "" {
		user = os.Getenv("MKBOX_REGISTRY_USER")
	}
	pass := os.Getenv("MKBOX_REGISTRY_PASSWORD")
	if passwordStdin {
		data, err := io.ReadAll(io.LimitReader(os.Stdin, 64<<10))
		if err != nil {
			return "", "", fmt.Errorf("读取标准输入密码: %w", err)
		}
		pass = strings.TrimSpace(string(data))
	}
	if user != "" && pass == "" {
		return "", "", fmt.Errorf("已指定用户名但缺少密码（使用 --password-stdin 或 MKBOX_REGISTRY_PASSWORD）")
	}
	return user, pass, nil
}

func newRegistryClient(user, pass string, plainHTTP bool) *bundle.Client {
	c := bundle.NewClient()
	c.Username = user
	c.Password = pass
	c.PlainHTTP = plainHTTP
	return c
}

func (a *App) bundlePushCmd() *cobra.Command {
	var (
		ref           string
		user          string
		passwordStdin bool
		plainHTTP     bool
		skipVerify    bool
		allowUnsigned bool
		asJSON        bool
	)
	cmd := &cobra.Command{
		Use:   "push <bundle.tar.gz>",
		Short: "将 bundle 作为 OCI artifact 推送到 registry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if ref == "" {
				return fmt.Errorf("必须指定 --ref，例如 ghcr.io/acme/mkbox:0.1.0")
			}
			reference, err := bundle.ParseReference(ref)
			if err != nil {
				return err
			}
			if !skipVerify {
				vr, err := bundle.VerifyFile(args[0], bundle.VerifyOptions{AllowUnsigned: allowUnsigned})
				if err != nil {
					return fmt.Errorf("推送前校验失败: %w", err)
				}
				for _, w := range vr.Warnings {
					logx.Warnf("%s", w)
				}
			}
			insp, err := bundle.Inspect(args[0])
			if err != nil {
				return err
			}
			u, p, err := registryCredentials(user, passwordStdin)
			if err != nil {
				return err
			}
			client := newRegistryClient(u, p, plainHTTP)
			meta := bundle.ArtifactMetadata{
				Name:    insp.Manifest.Name,
				Version: insp.Manifest.Version,
			}
			if insp.Signature != nil {
				meta.Signed = true
				meta.KeyID = insp.Signature.KeyID
			}
			res, err := client.PushFile(reference, args[0], meta)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(res)
			}
			logx.Successf("已推送 %s", res.Reference)
			logx.Item("bundle digest", res.BundleDigest)
			logx.Item("manifest digest", res.ManifestDigest)
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&ref, "ref", "", "目标引用，如 ghcr.io/acme/mkbox:0.1.0")
	fl.StringVar(&user, "user", "", "registry 用户名（或 MKBOX_REGISTRY_USER）")
	fl.BoolVar(&passwordStdin, "password-stdin", false, "从标准输入读取密码")
	fl.BoolVar(&plainHTTP, "plain-http", false, "使用 HTTP（仅限可信内网/本地 registry）")
	fl.BoolVar(&skipVerify, "skip-verify", false, "跳过推送前的 bundle 校验")
	fl.BoolVar(&allowUnsigned, "allow-unsigned", false, "允许推送未签名 bundle")
	fl.BoolVar(&asJSON, "json", false, "输出机器可读 JSON")
	return cmd
}

func (a *App) bundlePullCmd() *cobra.Command {
	var (
		ref           string
		output        string
		user          string
		passwordStdin bool
		plainHTTP     bool
		skipVerify    bool
		allowUnsigned bool
		asJSON        bool
	)
	cmd := &cobra.Command{
		Use:   "pull",
		Short: "从 registry 拉取 bundle 并校验摘要",
		RunE: func(cmd *cobra.Command, args []string) error {
			if ref == "" {
				return fmt.Errorf("必须指定 --ref")
			}
			reference, err := bundle.ParseReference(ref)
			if err != nil {
				return err
			}
			if output == "" {
				output = "bundle-" + strings.ReplaceAll(reference.Tag, "/", "-") + ".tar.gz"
			}
			u, p, err := registryCredentials(user, passwordStdin)
			if err != nil {
				return err
			}
			client := newRegistryClient(u, p, plainHTTP)
			res, err := client.PullFile(reference, output)
			if err != nil {
				return err
			}
			if !skipVerify {
				if _, err := bundle.VerifyFile(output, bundle.VerifyOptions{AllowUnsigned: allowUnsigned}); err != nil {
					return fmt.Errorf("拉取后校验失败: %w", err)
				}
			}
			if asJSON {
				return printJSON(res)
			}
			logx.Successf("已拉取 %s -> %s", res.Reference, res.Path)
			logx.Item("bundle digest", res.BundleDigest)
			logx.Item("大小", formatSize(res.Size))
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&ref, "ref", "", "源引用，如 ghcr.io/acme/mkbox:0.1.0")
	fl.StringVarP(&output, "output", "o", "", "输出文件（默认 bundle-<tag>.tar.gz）")
	fl.StringVar(&user, "user", "", "registry 用户名（或 MKBOX_REGISTRY_USER）")
	fl.BoolVar(&passwordStdin, "password-stdin", false, "从标准输入读取密码")
	fl.BoolVar(&plainHTTP, "plain-http", false, "使用 HTTP（仅限可信内网/本地 registry）")
	fl.BoolVar(&skipVerify, "skip-verify", false, "跳过拉取后的 bundle 校验")
	fl.BoolVar(&allowUnsigned, "allow-unsigned", false, "允许拉取未签名 bundle")
	fl.BoolVar(&asJSON, "json", false, "输出机器可读 JSON")
	return cmd
}
