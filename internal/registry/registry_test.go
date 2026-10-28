package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNormalize(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"docker.m.daocloud.io", "https://docker.m.daocloud.io", false},
		{"https://dockerproxy.net/", "https://dockerproxy.net", false},
		{"http://example.com/foo/?x=1#f", "http://example.com/foo", false},
		{"ftp://example.com", "", true},
		{"", "", true},
		{"https://", "", true},
	}
	for _, c := range cases {
		got, err := Normalize(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("Normalize(%q) expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("Normalize(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("Normalize(%q)=%q, want %q", c.in, got, c.want)
		}
	}
}

func TestDedupe(t *testing.T) {
	got := Dedupe([]string{
		"https://a.example",
		"https://a.example/",
		"a.example",
		"https://b.example",
		"bad url",
	})
	want := []string{"https://a.example", "https://b.example"}
	if len(got) != len(want) {
		t.Fatalf("Dedupe length=%d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Dedupe[%d]=%q, want %q", i, got[i], want[i])
		}
	}
}

func TestNormalizeList(t *testing.T) {
	got, err := NormalizeList([]string{"a.example", "https://a.example/", "https://b.example"})
	if err != nil {
		t.Fatalf("NormalizeList: %v", err)
	}
	if len(got) != 2 || got[0] != "https://a.example" || got[1] != "https://b.example" {
		t.Fatalf("NormalizeList = %v", got)
	}
	if _, err := NormalizeList([]string{"https://a.example", "://bad"}); err == nil {
		t.Fatal("invalid entry should fail the whole list")
	}
}

func TestCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	res := Check(context.Background(), srv.URL, 2*time.Second)
	if !res.OK {
		t.Fatalf("expected 401 to be considered reachable: %+v", res)
	}
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("status=%d, want 401", res.StatusCode)
	}
	if res.Latency <= 0 {
		t.Errorf("expected positive latency")
	}
}

func TestCheckFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	res := Check(context.Background(), srv.URL, 2*time.Second)
	if res.OK {
		t.Errorf("expected 500 to fail: %+v", res)
	}
}

func TestBestURLs(t *testing.T) {
	results := []Result{
		{URL: "https://slow", OK: true, Latency: 500 * time.Millisecond},
		{URL: "https://fast", OK: true, Latency: 20 * time.Millisecond},
		{URL: "https://dead", OK: false},
		{URL: "https://mid", OK: true, Latency: 100 * time.Millisecond},
	}
	got := BestURLs(results, 2)
	want := []string{"https://fast", "https://mid"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("BestURLs=%v, want %v", got, want)
	}
}

func TestFormatLatency(t *testing.T) {
	if got := FormatLatency(0); got != "-" {
		t.Errorf("FormatLatency(0)=%q", got)
	}
	if got := FormatLatency(500 * time.Microsecond); got != "<1ms" {
		t.Errorf("FormatLatency(500us)=%q", got)
	}
	if got := FormatLatency(150 * time.Millisecond); got != "150ms" {
		t.Errorf("FormatLatency(150ms)=%q", got)
	}
}
