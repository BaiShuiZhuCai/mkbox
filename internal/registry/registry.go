// Package registry probes container registry mirrors.
package registry

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Result is the outcome of probing a single mirror.
type Result struct {
	URL        string        `json:"url"`
	StatusCode int           `json:"statusCode"`
	Latency    time.Duration `json:"latency"`
	Err        string        `json:"error,omitempty"`
	OK         bool          `json:"ok"`
}

// Normalize canonicalises a mirror URL and requires http(s).
func Normalize(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("镜像地址不能为空")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("无效镜像地址 %q: %v", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("镜像地址 %q 必须使用 http 或 https", raw)
	}
	if u.Host == "" {
		return "", fmt.Errorf("镜像地址 %q 缺少主机名", raw)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// NormalizeList normalises every mirror and fails on the first invalid entry.
func NormalizeList(list []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(list))
	for _, raw := range list {
		n, err := Normalize(raw)
		if err != nil {
			return nil, err
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out, nil
}

// Dedupe normalises and de-duplicates a mirror list, preserving order.
func Dedupe(list []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(list))
	for _, raw := range list {
		n, err := Normalize(raw)
		if err != nil {
			continue
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// Candidates merges explicit mirrors with configured defaults.
func Candidates(explicit, configured []string) []string {
	all := make([]string, 0, len(explicit)+len(configured))
	all = append(all, explicit...)
	all = append(all, configured...)
	return Dedupe(all)
}

// Check probes a registry mirror's /v2/ endpoint.
func Check(ctx context.Context, mirror string, timeout time.Duration) Result {
	res := Result{URL: mirror}
	base, err := Normalize(mirror)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	res.URL = base
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, strings.TrimRight(base, "/")+"/v2/", nil)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	req.Header.Set("User-Agent", "makebox/1.0 (+https://github.com/makebox/makebox)")
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("重定向次数过多")
			}
			return nil
		},
	}
	start := time.Now()
	resp, err := client.Do(req)
	res.Latency = time.Since(start)
	if err != nil {
		res.Err = simplifyErr(err)
		return res
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	res.StatusCode = resp.StatusCode
	res.OK = resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusUnauthorized
	if !res.OK {
		res.Err = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return res
}

func simplifyErr(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "context deadline exceeded"), strings.Contains(s, "Client.Timeout"):
		return "超时"
	case strings.Contains(s, "no such host"):
		return "DNS 解析失败"
	case strings.Contains(s, "connection refused"):
		return "连接被拒绝"
	case strings.Contains(s, "certificate"):
		return "TLS 证书错误"
	case strings.Contains(s, "network is unreachable"):
		return "网络不可达"
	}
	return s
}

// CheckAll probes mirrors concurrently.
func CheckAll(ctx context.Context, mirrors []string, timeout time.Duration, concurrency int) []Result {
	if concurrency <= 0 {
		concurrency = 6
	}
	results := make([]Result, len(mirrors))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, m := range mirrors {
		wg.Add(1)
		go func(i int, m string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = Check(ctx, m, timeout)
		}(i, m)
	}
	wg.Wait()
	return results
}

// Working returns successful results sorted by latency.
func Working(results []Result) []Result {
	out := make([]Result, 0, len(results))
	for _, r := range results {
		if r.OK {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Latency < out[j].Latency })
	return out
}

// BestURLs returns up to n fastest working mirror URLs.
func BestURLs(results []Result, n int) []string {
	working := Working(results)
	if n > 0 && len(working) > n {
		working = working[:n]
	}
	out := make([]string, len(working))
	for i, r := range working {
		out[i] = r.URL
	}
	return out
}

// FormatLatency renders a latency in a compact human-readable form.
func FormatLatency(d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	if d < time.Millisecond {
		return "<1ms"
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.2fs", d.Seconds())
}
