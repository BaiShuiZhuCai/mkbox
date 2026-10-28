package cli

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

func validateCNI(cni string) error {
	switch cni {
	case "flannel", "calico", "none":
		return nil
	}
	return fmt.Errorf("--cni 必须是 flannel、calico 或 none")
}

// redactProxy hides the password portion of a proxy URL before printing it.
func redactProxy(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if u.User != nil {
		if _, has := u.User.Password(); has {
			u.User = url.UserPassword(u.User.Username(), "REDACTED")
		}
	}
	return u.String()
}

// applyProcessProxy makes subsequent HTTP clients in this process use the
// proxy, so that mirror probing and manifest downloads honour it immediately.
func applyProcessProxy(proxyURL string, noProxy []string) {
	keys := []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"}
	if proxyURL == "" {
		for _, k := range keys {
			_ = os.Unsetenv(k)
		}
		return
	}
	for _, k := range keys {
		_ = os.Setenv(k, proxyURL)
	}
	if len(noProxy) > 0 {
		joined := strings.Join(noProxy, ",")
		_ = os.Setenv("NO_PROXY", joined)
		_ = os.Setenv("no_proxy", joined)
	}
}

func envProxy() string {
	for _, key := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}

func formatSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}
