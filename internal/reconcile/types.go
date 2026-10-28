// Package reconcile turns mkbox's imperative commands into a small,
// inspectable convergence engine: observe, plan, apply, diff and rollback.
package reconcile

import (
	"fmt"
	"strings"
	"time"

	"github.com/makebox/makebox/internal/config"
)

// Scope selects which part of the desired state is managed.
type Scope string

const (
	ScopeDocker Scope = "docker"
	ScopeK8s    Scope = "k8s"
	ScopeAll    Scope = "all"
)

// ParseScope validates a scope name.
func ParseScope(raw string) (Scope, error) {
	switch Scope(strings.ToLower(strings.TrimSpace(raw))) {
	case ScopeDocker:
		return ScopeDocker, nil
	case ScopeK8s:
		return ScopeK8s, nil
	case ScopeAll, "":
		return ScopeAll, nil
	}
	return "", fmt.Errorf("scope 必须是 docker、k8s 或 all")
}

func (s Scope) includesDocker() bool { return s == ScopeDocker || s == ScopeAll }
func (s Scope) includesK8s() bool    { return s == ScopeK8s || s == ScopeAll }

// DockerProfile is the desired Docker state.
type DockerProfile struct {
	InstallMirror string   `json:"installMirror"`
	Mirrors       []string `json:"mirrors"`
	LogDriver     string   `json:"logDriver"`
	LogMaxSize    string   `json:"logMaxSize"`
	LogMaxFile    string   `json:"logMaxFile"`
	DataRoot      string   `json:"dataRoot"`
}

// K8sProfile is the desired Kubernetes state.
type K8sProfile struct {
	Distro               string `json:"distro"`
	Version              string `json:"version"`
	RepoMirror           string `json:"repoMirror"`
	ImageRepository      string `json:"imageRepository"`
	PodCIDR              string `json:"podCIDR"`
	ServiceCIDR          string `json:"serviceCIDR"`
	CNI                  string `json:"cni"`
	APIAddress           string `json:"apiAddress,omitempty"`
	ControlPlaneEndpoint string `json:"controlPlaneEndpoint,omitempty"`
	SkipPreflight        bool   `json:"skipPreflight"`
	SkipFirewall         bool   `json:"skipFirewall"`
	SkipAddons           bool   `json:"skipAddons"`
	GHProxy              string `json:"ghProxy,omitempty"`
}

// ProxyProfile is the desired daemon proxy state.
type ProxyProfile struct {
	URL     string   `json:"url,omitempty"`
	NoProxy []string `json:"noProxy,omitempty"`
}

// Profile is the full desired state.
type Profile struct {
	Scope  Scope         `json:"scope"`
	Docker DockerProfile `json:"docker"`
	K8s    K8sProfile    `json:"k8s"`
	Proxy  ProxyProfile  `json:"proxy"`
}

// ProfileFromConfig builds a profile from mkbox configuration.
func ProfileFromConfig(cfg *config.Config, scope Scope) Profile {
	p := Profile{Scope: scope}
	if cfg == nil {
		return p
	}
	p.Docker = DockerProfile{
		InstallMirror: cfg.Docker.InstallMirror,
		Mirrors:       append([]string(nil), cfg.Registry.Mirrors...),
		LogDriver:     cfg.Docker.LogDriver,
		LogMaxSize:    cfg.Docker.LogMaxSize,
		LogMaxFile:    cfg.Docker.LogMaxFile,
		DataRoot:      cfg.Docker.DataRoot,
	}
	p.K8s = K8sProfile{
		Distro:          cfg.K8s.Distro,
		Version:         cfg.K8s.Version,
		RepoMirror:      cfg.K8s.RepoMirror,
		ImageRepository: cfg.K8s.ImageRepository,
		PodCIDR:         cfg.K8s.PodCIDR,
		ServiceCIDR:     cfg.K8s.ServiceCIDR,
		CNI:             cfg.K8s.CNI,
		GHProxy:         cfg.Network.GHProxy,
	}
	p.Proxy = ProxyProfile{
		URL:     cfg.EffectiveProxy(),
		NoProxy: append([]string(nil), cfg.Proxy.NoProxy...),
	}
	return p
}

// Action is one planned change.
type Action struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Reason     string   `json:"reason,omitempty"`
	Risk       string   `json:"risk"` // low, medium, high
	Reversible bool     `json:"reversible"`
	Details    []string `json:"details,omitempty"`
}

// FileDiff describes a managed file that differs from the desired content.
type FileDiff struct {
	Path    string `json:"path"`
	Exists  bool   `json:"exists"`
	Changed bool   `json:"changed"`
	Unified string `json:"unified,omitempty"`
	Note    string `json:"note,omitempty"`
}

// Plan is the complete convergence plan.
type Plan struct {
	GeneratedAt string     `json:"generatedAt"`
	Scope       Scope      `json:"scope"`
	Host        HostInfo   `json:"host"`
	Profile     Profile    `json:"desired"`
	Observed    Observed   `json:"observed"`
	Actions     []Action   `json:"actions,omitempty"`
	Diffs       []FileDiff `json:"diffs,omitempty"`
	Warnings    []string   `json:"warnings,omitempty"`
}

// Empty reports whether no changes are required.
func (p Plan) Empty() bool { return len(p.Actions) == 0 }

// HasIrreversible reports whether the plan contains non-rollbackable actions.
func (p Plan) HasIrreversible() bool {
	for _, a := range p.Actions {
		if !a.Reversible {
			return true
		}
	}
	return false
}

// IrreversibleActions returns titles of non-rollbackable actions.
func (p Plan) IrreversibleActions() []string {
	var out []string
	for _, a := range p.Actions {
		if !a.Reversible {
			out = append(out, a.Title)
		}
	}
	return out
}

// HostInfo is a small host fingerprint included in plans and reports.
type HostInfo struct {
	Hostname    string `json:"hostname"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	Kernel      string `json:"kernel"`
	IsRoot      bool   `json:"isRoot"`
	GeneratedBy string `json:"generatedBy,omitempty"`
}

// PlanOptions tunes plan generation.
type PlanOptions struct {
	SkipPreflight bool
	Now           time.Time
}

// Service names recorded for rollback restarts.
const (
	ServiceDocker     = "docker"
	ServiceContainerd = "containerd"
	ServiceKubelet    = "kubelet"
	ServiceK3s        = "k3s"
)
