// Package system detects Linux distribution details and provides safe
// filesystem helpers used by all mkbox commands.
package system

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/makebox/makebox/internal/logx"
)

// User describes the invoking (sudo) user.
type User struct {
	Name string
	UID  int
	GID  int
	Home string
}

// OS describes the detected Linux environment.
type OS struct {
	ID         string
	IDLike     []string
	VersionID  string
	Codename   string
	PrettyName string
	Kernel     string
	Arch       string
	Family     string
	PkgMgr     string
	ServiceMgr string
	IsRoot     bool
	SudoUser   *User
}

// ParseOSRelease parses /etc/os-release style content.
func ParseOSRelease(content string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
			v = v[1 : len(v)-1]
		}
		v = strings.ReplaceAll(v, `\"`, `"`)
		out[strings.TrimSpace(k)] = v
	}
	return out
}

// Classify maps distro identifiers to a package-manager family.
func Classify(id string, like []string) (family, pkgMgr string) {
	ids := append([]string{strings.ToLower(id)}, like...)
	for i := range ids {
		ids[i] = strings.ToLower(strings.TrimSpace(ids[i]))
	}
	has := func(want string) bool {
		for _, v := range ids {
			if v == want {
				return true
			}
		}
		return false
	}
	hasLike := func(want string) bool {
		for _, v := range ids {
			if strings.Contains(v, want) {
				return true
			}
		}
		return false
	}

	switch {
	case has("alpine"):
		return "alpine", "apk"
	case has("arch"), has("manjaro"), has("endeavouros"), has("garuda"), has("artix"):
		return "arch", "pacman"
	case has("opensuse-leap"), has("opensuse-tumbleweed"), has("opensuse"), has("sles"), has("sled"), hasLike("opensuse"):
		return "suse", "zypper"
	case has("rhel"), has("centos"), has("rocky"), has("almalinux"), has("fedora"), has("ol"),
		has("amzn"), has("cloudlinux"), has("openeuler"), has("opencloudos"), has("anolis"),
		hasLike("rhel"), hasLike("fedora"), hasLike("centos"):
		return "rhel", "dnf"
	case has("ubuntu"), has("debian"), has("linuxmint"), has("raspbian"), has("kali"),
		has("pop"), has("elementary"), has("zorin"), has("neon"), has("deepin"),
		hasLike("ubuntu"), hasLike("debian"):
		return "debian", "apt"
	}
	return "unknown", ""
}

func commandExists(name string) bool {
	if p, err := exec.LookPath(name); err == nil && p != "" {
		return true
	}
	for _, dir := range []string{"/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin"} {
		if fileExists(filepath.Join(dir, name)) {
			return true
		}
	}
	return false
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// Detect inspects the host and returns an OS description.
func Detect() *OS {
	o := &OS{
		IsRoot: os.Geteuid() == 0,
		Arch:   runtime.GOARCH,
	}
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		o.Kernel = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		m := ParseOSRelease(string(b))
		o.ID = m["ID"]
		o.PrettyName = m["PRETTY_NAME"]
		o.VersionID = m["VERSION_ID"]
		o.Codename = m["VERSION_CODENAME"]
		if o.Codename == "" {
			o.Codename = m["UBUNTU_CODENAME"]
		}
		if like := m["ID_LIKE"]; like != "" {
			o.IDLike = strings.Fields(like)
		}
	}
	o.Family, o.PkgMgr = Classify(o.ID, o.IDLike)
	if o.PkgMgr == "" {
		switch {
		case commandExists("apt-get"):
			o.Family, o.PkgMgr = "debian", "apt"
		case commandExists("dnf"):
			o.Family, o.PkgMgr = "rhel", "dnf"
		case commandExists("yum"):
			o.Family, o.PkgMgr = "rhel", "yum"
		case commandExists("zypper"):
			o.Family, o.PkgMgr = "suse", "zypper"
		case commandExists("pacman"):
			o.Family, o.PkgMgr = "arch", "pacman"
		case commandExists("apk"):
			o.Family, o.PkgMgr = "alpine", "apk"
		}
	}
	if o.Family == "rhel" && !commandExists("dnf") && commandExists("yum") {
		o.PkgMgr = "yum"
	}
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		o.ServiceMgr = "systemd"
	} else if commandExists("rc-service") {
		o.ServiceMgr = "openrc"
	}
	if u := SudoUser(); u != nil {
		o.SudoUser = u
	}
	return o
}

// SudoUser returns the user that invoked sudo, if any.
func SudoUser() *User {
	name := os.Getenv("SUDO_USER")
	if name == "" || name == "root" {
		return nil
	}
	u, err := user.Lookup(name)
	if err != nil {
		return nil
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	return &User{Name: u.Username, UID: uid, GID: gid, Home: u.HomeDir}
}

// DisplayName returns a human friendly distro name.
func (o *OS) DisplayName() string {
	if o.PrettyName != "" {
		return o.PrettyName
	}
	if o.ID != "" {
		return o.ID + " " + o.VersionID
	}
	return "unknown Linux"
}

// IsSupported reports whether mkbox can automate this distro.
func (o *OS) IsSupported() bool {
	return o.Family != "unknown" && o.PkgMgr != ""
}

// PackageManager returns the package manager command.
func (o *OS) PackageManager() string { return o.PkgMgr }

// HasSystemd reports whether systemd is the service manager.
func (o *OS) HasSystemd() bool { return o.ServiceMgr == "systemd" }

// CgroupVersion reports the cgroup hierarchy in use: "v2" when the unified
// hierarchy is mounted at /sys/fs/cgroup, "v1" otherwise.
func CgroupVersion() string {
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err == nil {
		return "v2"
	}
	return "v1"
}

// HasCgroupV2 reports whether the host runs a unified cgroup v2 hierarchy.
func HasCgroupV2() bool { return CgroupVersion() == "v2" }

// PrimaryIP returns the IPv4 address used for outbound traffic.
func PrimaryIP() (string, error) {
	if conn, err := net.Dial("udp", "1.1.1.1:53"); err == nil {
		defer conn.Close()
		if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok && addr.IP != nil && !addr.IP.IsLoopback() {
			return addr.IP.String(), nil
		}
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil {
				return ipnet.IP.String(), nil
			}
		}
	}
	return "", fmt.Errorf("no non-loopback IPv4 address found")
}

// MemoryMB returns total system memory in MiB.
func MemoryMB() int {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				if kb, err := strconv.Atoi(fields[1]); err == nil {
					return kb / 1024
				}
			}
		}
	}
	return 0
}

// DiskFreeGB returns free disk space in GiB for the filesystem containing path.
func DiskFreeGB(path string) float64 {
	for {
		var st syscall.Statfs_t
		if err := syscall.Statfs(path, &st); err == nil {
			return float64(st.Bavail*uint64(st.Bsize)) / (1 << 30)
		}
		parent := filepath.Dir(path)
		if parent == path || parent == "." {
			return 0
		}
		path = parent
	}
}

// PortInUse reports whether a TCP port is already bound on this host.
func PortInUse(port int) bool {
	ln, err := net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return true
	}
	_ = ln.Close()
	return false
}

func fileMode(p string, def os.FileMode) os.FileMode {
	if st, err := os.Stat(p); err == nil {
		return st.Mode().Perm()
	}
	return def
}

func timestamp() string {
	return time.Now().Format("20060102-150405")
}

func fileExistsStat(p string) (os.FileInfo, bool) {
	st, err := os.Stat(p)
	if err != nil {
		return nil, false
	}
	return st, true
}

func copyFile(src, dst string, mode os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, mode); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}

// ChownToSudoUser changes ownership of path to the sudo-invoking user.
func ChownToSudoUser(path string) error {
	u := SudoUser()
	if u == nil {
		return nil
	}
	return os.Chown(path, u.UID, u.GID)
}

// Chown changes ownership of path.
func Chown(path string, uid, gid int) error {
	return os.Chown(path, uid, gid)
}

// UserHome returns the home directory for the real invoking user.
func UserHome() string {
	if u := SudoUser(); u != nil && u.Home != "" {
		return u.Home
	}
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "/root"
}

// BackupPath computes the backup filename for path.
func BackupPath(path string) string {
	p := fmt.Sprintf("%s.mkbox.bak.%s", path, timestamp())
	if _, err := os.Stat(p); err == nil {
		p = fmt.Sprintf("%s.%d", p, time.Now().UnixNano()%1e6)
	}
	return p
}

// PrintPath prints a path with the dry-run marker when applicable.
func PrintPath(dry bool, verb, path string) {
	if dry {
		logx.Infof("[dry-run] %s %s", verb, path)
		return
	}
	logx.Debugf(true, "%s %s", verb, path)
}
