// Package netcheck tests connectivity to services mkbox depends on.
package netcheck

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Endpoint describes a connectivity target.
type Endpoint struct {
	Name string
	URL  string
}

// Result is a connectivity probe result.
type Result struct {
	Name       string
	URL        string
	OK         bool
	StatusCode int
	Latency    time.Duration
	Err        string
}

// DefaultEndpoints are the services used during Docker/Kubernetes bootstrap.
var DefaultEndpoints = []Endpoint{
	{"Docker Hub", "https://registry-1.docker.io/v2/"},
	{"Kubernetes registry", "https://registry.k8s.io/v2/"},
	{"Kubernetes packages", "https://pkgs.k8s.io/"},
	{"GitHub", "https://github.com/"},
	{"Rancher (k3s)", "https://get.k3s.io/"},
	{"Rancher mirror (CN)", "https://rancher-mirror.rancher.cn/"},
	{"Go module proxy (CN)", "https://goproxy.cn/"},
	{"Aliyun mirror", "https://mirrors.aliyun.com/"},
}

// Check probes an HTTP endpoint. Any HTTP response below 500 counts as
// reachable; 401/403/404 still prove DNS, TCP and TLS work.
func Check(ctx context.Context, ep Endpoint, timeout time.Duration) Result {
	res := Result{Name: ep.Name, URL: ep.URL}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, ep.URL, nil)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	req.Header.Set("User-Agent", "makebox/1.0")
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   timeout,
			KeepAlive: 5 * time.Second,
		}).DialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
	}
	client := &http.Client{Transport: transport, Timeout: timeout}
	start := time.Now()
	resp, err := client.Do(req)
	res.Latency = time.Since(start)
	if err != nil {
		res.Err = friendly(err)
		return res
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 2048))
	res.StatusCode = resp.StatusCode
	res.OK = resp.StatusCode < 500
	if !res.OK {
		res.Err = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return res
}

func friendly(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "context deadline exceeded"), strings.Contains(s, "Client.Timeout"), strings.Contains(s, "i/o timeout"):
		return "超时"
	case strings.Contains(s, "no such host"):
		return "DNS 解析失败"
	case strings.Contains(s, "connection refused"):
		return "连接被拒绝"
	case strings.Contains(s, "certificate"):
		return "TLS 证书错误"
	case strings.Contains(s, "network is unreachable"):
		return "网络不可达"
	case strings.Contains(s, "proxyconnect"):
		return "代理不可用"
	}
	return s
}

// CheckAll probes endpoints concurrently, preserving order.
func CheckAll(ctx context.Context, eps []Endpoint, timeout time.Duration) []Result {
	results := make([]Result, len(eps))
	var wg sync.WaitGroup
	for i, ep := range eps {
		wg.Add(1)
		go func(i int, ep Endpoint) {
			defer wg.Done()
			results[i] = Check(ctx, ep, timeout)
		}(i, ep)
	}
	wg.Wait()
	return results
}

// OKCount returns the number of successful probes.
func OKCount(results []Result) int {
	n := 0
	for _, r := range results {
		if r.OK {
			n++
		}
	}
	return n
}
