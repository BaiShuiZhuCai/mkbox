// Package mirror switches OS package mirrors and developer tool registries.
package mirror

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/makebox/makebox/internal/execx"
	"github.com/makebox/makebox/internal/logx"
	"github.com/makebox/makebox/internal/system"
)

// Source describes a mirror site.
type Source struct {
	ID             string
	Name           string
	Ubuntu         string
	Debian         string
	DebianSecurity string
	Arch           string
	Alpine         string
}

// Sources lists supported mirror sites.
var Sources = []Source{
	{
		ID:             "aliyun",
		Name:           "阿里云",
		Ubuntu:         "https://mirrors.aliyun.com/ubuntu/",
		Debian:         "https://mirrors.aliyun.com/debian/",
		DebianSecurity: "https://mirrors.aliyun.com/debian-security/",
		Arch:           "https://mirrors.aliyun.com/archlinux/$repo/os/$arch",
		Alpine:         "https://mirrors.aliyun.com/alpine/",
	},
	{
		ID:             "tuna",
		Name:           "清华大学",
		Ubuntu:         "https://mirrors.tuna.tsinghua.edu.cn/ubuntu/",
		Debian:         "https://mirrors.tuna.tsinghua.edu.cn/debian/",
		DebianSecurity: "https://mirrors.tuna.tsinghua.edu.cn/debian-security/",
		Arch:           "https://mirrors.tuna.tsinghua.edu.cn/archlinux/$repo/os/$arch",
		Alpine:         "https://mirrors.tuna.tsinghua.edu.cn/alpine/",
	},
	{
		ID:             "ustc",
		Name:           "中科大",
		Ubuntu:         "https://mirrors.ustc.edu.cn/ubuntu/",
		Debian:         "https://mirrors.ustc.edu.cn/debian/",
		DebianSecurity: "https://mirrors.ustc.edu.cn/debian-security/",
		Arch:           "https://mirrors.ustc.edu.cn/archlinux/$repo/os/$arch",
		Alpine:         "https://mirrors.ustc.edu.cn/alpine/",
	},
	{
		ID:             "huawei",
		Name:           "华为云",
		Ubuntu:         "https://repo.huaweicloud.com/ubuntu/",
		Debian:         "https://repo.huaweicloud.com/debian/",
		DebianSecurity: "https://repo.huaweicloud.com/debian-security/",
		Arch:           "https://repo.huaweicloud.com/archlinux/$repo/os/$arch",
		Alpine:         "https://repo.huaweicloud.com/alpine/",
	},
}

// GetSource looks up a mirror by ID.
func GetSource(id string) (Source, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, s := range Sources {
		if s.ID == id {
			return s, true
		}
	}
	return Source{}, false
}

var codenameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func components(distro string, withFirmware bool) string {
	if distro == "ubuntu" {
		return "main restricted universe multiverse"
	}
	c := "main contrib non-free"
	if withFirmware {
		c += " non-free-firmware"
	}
	return c
}

// UbuntuSources renders a classic /etc/apt/sources.list for Ubuntu.
func UbuntuSources(codename string, src Source) (string, error) {
	if !codenameRe.MatchString(codename) {
		return "", fmt.Errorf("无效的 Ubuntu 代号 %q", codename)
	}
	c := components("ubuntu", false)
	suites := []string{codename, codename + "-updates", codename + "-backports", codename + "-security"}
	var b strings.Builder
	b.WriteString("# Managed by makebox (mkbox)\n")
	for _, suite := range suites {
		fmt.Fprintf(&b, "deb %s %s %s\n", src.Ubuntu, suite, c)
	}
	return b.String(), nil
}

// UbuntuDEB822 renders Ubuntu's deb822 sources file.
func UbuntuDEB822(codename string, src Source) (string, error) {
	if !codenameRe.MatchString(codename) {
		return "", fmt.Errorf("无效的 Ubuntu 代号 %q", codename)
	}
	suites := strings.Join([]string{codename, codename + "-updates", codename + "-backports", codename + "-security"}, " ")
	return fmt.Sprintf(`# Managed by makebox (mkbox)
Types: deb
URIs: %s
Suites: %s
Components: %s
Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg
`, src.Ubuntu, suites, components("ubuntu", false)), nil
}

// DebianSources renders a classic /etc/apt/sources.list for Debian.
func DebianSources(codename string, src Source, withFirmware bool) (string, error) {
	if !codenameRe.MatchString(codename) {
		return "", fmt.Errorf("无效的 Debian 代号 %q", codename)
	}
	c := components("debian", withFirmware)
	var b strings.Builder
	b.WriteString("# Managed by makebox (mkbox)\n")
	for _, suite := range []string{codename, codename + "-updates", codename + "-backports"} {
		fmt.Fprintf(&b, "deb %s %s %s\n", src.Debian, suite, c)
	}
	fmt.Fprintf(&b, "deb %s %s-security %s\n", src.DebianSecurity, codename, c)
	return b.String(), nil
}

// DebianDEB822 renders Debian's deb822 sources file.
func DebianDEB822(codename string, src Source, withFirmware bool) (string, error) {
	if !codenameRe.MatchString(codename) {
		return "", fmt.Errorf("无效的 Debian 代号 %q", codename)
	}
	c := components("debian", withFirmware)
	var b strings.Builder
	b.WriteString("# Managed by makebox (mkbox)\n")
	fmt.Fprintf(&b, "Types: deb\nURIs: %s\nSuites: %s %s-updates %s-backports\nComponents: %s\nSigned-By: /usr/share/keyrings/debian-archive-keyring.gpg\n",
		src.Debian, codename, codename, codename, c)
	fmt.Fprintf(&b, "\nTypes: deb\nURIs: %s\nSuites: %s-security\nComponents: %s\nSigned-By: /usr/share/keyrings/debian-archive-keyring.gpg\n",
		src.DebianSecurity, codename, c)
	return b.String(), nil
}

// PrependArchMirror adds a mirror server at the top of pacman's mirrorlist.
func PrependArchMirror(content, server string) string {
	if strings.Contains(content, "Server = "+server) {
		return content
	}
	header := "# Managed by makebox (mkbox)\nServer = " + server + "\n\n"
	return header + content
}

// ReplaceAlpineMirror rewrites mirror hosts in /etc/apk/repositories.
func ReplaceAlpineMirror(content string, src Source) string {
	out := content
	oldHosts := []string{
		"https://dl-cdn.alpinelinux.org/alpine",
		"http://dl-cdn.alpinelinux.org/alpine",
	}
	for _, s := range Sources {
		oldHosts = append(oldHosts, strings.TrimRight(s.Alpine, "/"))
	}
	newBase := strings.TrimRight(src.Alpine, "/")
	for _, old := range oldHosts {
		out = strings.ReplaceAll(out, old, newBase)
	}
	return out
}

// Manager applies OS mirror changes.
type Manager struct {
	R  *execx.Runner
	FS *system.FS
	OS *system.OS
}

// NewManager creates a mirror manager.
func NewManager(r *execx.Runner, fs *system.FS, osInfo *system.OS) *Manager {
	return &Manager{R: r, FS: fs, OS: osInfo}
}

// SetOS switches the distribution package mirror.
func (m *Manager) SetOS(id string) error {
	src, ok := GetSource(id)
	if !ok {
		return fmt.Errorf("未知镜像站 %q，可选: aliyun, tuna, ustc, huawei", id)
	}
	if !m.OS.IsRoot && !m.R.DryRun {
		return fmt.Errorf("此操作需要 root 权限，请使用 sudo 运行")
	}
	logx.Header("切换系统软件源: %s", src.Name)

	switch m.OS.Family {
	case "debian":
		return m.setDebian(src)
	case "arch":
		return m.setArch(src)
	case "alpine":
		return m.setAlpine(src)
	case "rhel", "suse":
		return fmt.Errorf("%s 的软件源结构因发行版而异，暂未自动切换；Docker/K8s 镜像加速不受影响", m.OS.DisplayName())
	default:
		return fmt.Errorf("暂不支持 %s 的软件源切换", m.OS.DisplayName())
	}
}

func (m *Manager) setDebian(src Source) error {
	distro := "debian"
	switch m.OS.ID {
	case "ubuntu", "raspbian":
		distro = "ubuntu"
	default:
		for _, like := range m.OS.IDLike {
			if strings.Contains(like, "ubuntu") {
				distro = "ubuntu"
			}
		}
		if m.OS.ID == "linuxmint" {
			distro = "ubuntu"
		}
	}
	if m.OS.Codename == "" {
		return fmt.Errorf("无法读取系统代号，请检查 /etc/os-release")
	}

	deb822Path := "/etc/apt/sources.list.d/ubuntu.sources"
	classicPath := "/etc/apt/sources.list"
	if distro == "debian" {
		deb822Path = "/etc/apt/sources.list.d/debian.sources"
	}

	hasDeb822 := fileExists(deb822Path)
	var content string
	var err error
	if distro == "ubuntu" {
		if hasDeb822 {
			content, err = UbuntuDEB822(m.OS.Codename, src)
		} else {
			content, err = UbuntuSources(m.OS.Codename, src)
		}
	} else {
		withFirmware := debianHasFirmware(m.OS.VersionID)
		if hasDeb822 {
			content, err = DebianDEB822(m.OS.Codename, src, withFirmware)
		} else {
			content, err = DebianSources(m.OS.Codename, src, withFirmware)
		}
	}
	if err != nil {
		return err
	}
	target := classicPath
	if hasDeb822 {
		target = deb822Path
	}
	if err := m.FS.Write(target, []byte(content), 0o644); err != nil {
		return err
	}
	logx.Successf("已更新 %s", target)
	if hasDeb822 && fileExists(classicPath) {
		if data, err := os.ReadFile(classicPath); err == nil && hasActiveDeb(string(data)) {
			logx.Warnf("%s 仍包含 deb 条目，可能与新配置重复；原文件已备份，请按需清理", classicPath)
		}
	}
	if err := m.R.RunEnv([]string{"DEBIAN_FRONTEND=noninteractive"}, "apt-get", "update"); err != nil {
		return fmt.Errorf("apt-get update 失败，请检查镜像站可用性: %w", err)
	}
	logx.Successf("软件源已切换为 %s", src.Name)
	return nil
}

func hasActiveDeb(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "deb ") || strings.HasPrefix(trimmed, "deb-src ") {
			return true
		}
	}
	return false
}

func debianHasFirmware(version string) bool {
	if version == "" {
		return true
	}
	major := 0
	if _, err := fmt.Sscanf(version, "%d", &major); err != nil {
		return true
	}
	return major >= 12
}

func (m *Manager) setArch(src Source) error {
	path := "/etc/pacman.d/mirrorlist"
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取 %s: %w", path, err)
	}
	updated := PrependArchMirror(string(data), src.Arch)
	if updated == string(data) {
		logx.Infof("镜像站已存在于 mirrorlist，无需修改")
		return nil
	}
	if err := m.FS.Write(path, []byte(updated), 0o644); err != nil {
		return err
	}
	logx.Successf("已更新 %s，建议执行 pacman -Syu", path)
	return nil
}

func (m *Manager) setAlpine(src Source) error {
	path := "/etc/apk/repositories"
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取 %s: %w", path, err)
	}
	updated := ReplaceAlpineMirror(string(data), src)
	if updated == string(data) {
		logx.Infof("镜像站已是 %s，无需修改", src.Name)
		return nil
	}
	if err := m.FS.Write(path, []byte(updated), 0o644); err != nil {
		return err
	}
	if err := m.R.Run("apk", "update"); err != nil {
		return fmt.Errorf("apk update 失败: %w", err)
	}
	logx.Successf("软件源已切换为 %s", src.Name)
	return nil
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// SetDev configures pip, npm and Go module mirrors.
func (m *Manager) SetDev(pip, npm, gomod bool) error {
	if !m.OS.IsRoot && !m.R.DryRun {
		return fmt.Errorf("此操作需要 root 权限，请使用 sudo 运行")
	}
	if pip {
		content := "[global]\nindex-url = https://mirrors.aliyun.com/pypi/simple/\ntrusted-host = mirrors.aliyun.com\n"
		if err := m.FS.Write("/etc/pip.conf", []byte(content), 0o644); err != nil {
			return err
		}
		logx.Successf("pip 已切换到阿里云 PyPI 镜像")
	}
	if npm {
		if execx.Has("npm") {
			if err := m.R.Run("npm", "config", "set", "registry", "https://registry.npmmirror.com", "--global"); err != nil {
				return fmt.Errorf("配置 npm 镜像失败: %w", err)
			}
			logx.Successf("npm 已切换到 npmmirror")
		} else {
			logx.Warnf("未安装 npm，跳过")
		}
	}
	if gomod {
		if execx.Has("go") {
			if err := m.R.Run("go", "env", "-w", "GOPROXY=https://goproxy.cn,direct"); err != nil {
				return fmt.Errorf("配置 Go 模块代理失败: %w", err)
			}
		} else {
			logx.Warnf("未安装 Go，仅写入 shell 环境文件")
		}
		content := "# Managed by makebox (mkbox)\nexport GOPROXY='https://goproxy.cn,direct'\nexport GOSUMDB='sum.golang.google.cn'\n"
		if err := m.FS.Write("/etc/profile.d/mkbox-go.sh", []byte(content), 0o644); err != nil {
			return err
		}
		logx.Successf("Go 模块代理已切换到 goproxy.cn（新登录会话生效）")
	}
	return nil
}

// DetectOSMirror inspects current configuration and returns a mirror ID.
func DetectOSMirror(osInfo *system.OS) string {
	var paths []string
	switch osInfo.Family {
	case "debian":
		paths = []string{"/etc/apt/sources.list", "/etc/apt/sources.list.d/ubuntu.sources", "/etc/apt/sources.list.d/debian.sources"}
	case "arch":
		paths = []string{"/etc/pacman.d/mirrorlist"}
	case "alpine":
		paths = []string{"/etc/apk/repositories"}
	default:
		return ""
	}
	var content strings.Builder
	for _, p := range paths {
		if data, err := os.ReadFile(p); err == nil {
			content.Write(data)
		}
	}
	text := content.String()
	for _, src := range Sources {
		for _, base := range []string{src.Ubuntu, src.Debian, src.Arch, src.Alpine} {
			host := strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://")
			host = strings.SplitN(host, "/", 2)[0]
			if host != "" && strings.Contains(text, host) {
				return src.ID
			}
		}
	}
	return ""
}
