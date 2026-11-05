package justcode

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestV2HealthRejectsLegacyAndMisleadingBodies(t *testing.T) {
	for _, body := range []string{
		`{"healthy":true}`, `{"status":"unhealthy"}`, "healthy", "<html>healthy</html>",
		`{"version":"1.19.5","pid":1,"urls":[],"paths":{"tmp":"/tmp"}}`,
		`{"version":"2.2.5"}`, `{"version":"2.2.5","pid":-1,"urls":[],"paths":{"tmp":"/tmp"}}`,
	} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer server.Close()
			if probe := ProbeHealth(context.Background(), nil, server.URL, DefaultUsername, "pw"); probe.Healthy {
				t.Fatalf("non-v2 readiness accepted: %s", body)
			}
		})
	}
}

func TestWaitHealthyImmediate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/info" {
			t.Errorf("path = %q, want /api/info", r.URL.Path)
		}
		u, p, ok := r.BasicAuth()
		if !ok || u != "opencode" || p != "pw" {
			t.Errorf("basic auth = %q:%q (ok=%v), want opencode:pw", u, p, ok)
		}
		_, _ = w.Write([]byte(`{"version":"2.2.5","pid":1,"urls":[],"paths":{"tmp":"/tmp/opencode"}}`))
	}))
	defer srv.Close()

	cfg := HealthConfig{Deadline: time.Second, PollInterval: time.Millisecond, RequestTimeout: time.Second}
	if err := WaitHealthy(context.Background(), srv.URL, "opencode", "pw", cfg, nil); err != nil {
		t.Fatalf("WaitHealthy: %v", err)
	}
}

func TestWaitHealthyEmptyPassword(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, p, ok := r.BasicAuth()
		if !ok || p != "" {
			t.Errorf("password = %q (ok=%v), want empty (no auth)", p, ok)
		}
		_, _ = w.Write([]byte(`{"version":"2.2.5","pid":1,"urls":[],"paths":{"tmp":"/tmp/opencode"}}`))
	}))
	defer srv.Close()

	cfg := HealthConfig{Deadline: time.Second, PollInterval: time.Millisecond, RequestTimeout: time.Second}
	if err := WaitHealthy(context.Background(), srv.URL, "opencode", "", cfg, nil); err != nil {
		t.Fatalf("WaitHealthy: %v", err)
	}
}

func TestWaitHealthyDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"starting"}`)) // never healthy
	}))
	defer srv.Close()

	cfg := HealthConfig{Deadline: 60 * time.Millisecond, PollInterval: 5 * time.Millisecond, RequestTimeout: time.Second}
	start := time.Now()
	err := WaitHealthy(context.Background(), srv.URL, "opencode", "pw", cfg, nil)
	if err == nil || !strings.Contains(err.Error(), "did not become healthy") {
		t.Fatalf("expected deadline error, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("deadline not respected: took %v", elapsed)
	}
}

func TestWaitHealthyPerRequestTimeout(t *testing.T) {
	// A handler that hangs longer than RequestTimeout must not stall the wait:
	// each probe times out individually and the wall-clock deadline still wins.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(800 * time.Millisecond)
		_, _ = w.Write([]byte(`{"version":"2.2.5","pid":1,"urls":[],"paths":{"tmp":"/tmp/opencode"}}`))
	}))
	t.Cleanup(func() {
		srv.CloseClientConnections()
		srv.Close()
	})

	cfg := HealthConfig{Deadline: 150 * time.Millisecond, PollInterval: 10 * time.Millisecond, RequestTimeout: 50 * time.Millisecond}
	start := time.Now()
	err := WaitHealthy(context.Background(), srv.URL, "opencode", "pw", cfg, nil)
	if err == nil {
		t.Fatal("expected an error from a hung endpoint")
	}
	if elapsed := time.Since(start); elapsed > 600*time.Millisecond {
		t.Fatalf("hung request bypassed deadline: took %v", elapsed)
	}
}

func TestProbeHealthReportsStatus(t *testing.T) {
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":"2.2.5","pid":1,"urls":[],"paths":{"tmp":"/tmp/opencode"}}`))
	}))
	defer healthy.Close()
	if p := ProbeHealth(context.Background(), nil, healthy.URL, "opencode", "pw"); !p.Healthy || p.Status != 200 {
		t.Fatalf("probe = %+v, want healthy 200", p)
	}

	unauthorized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("unauthorized"))
	}))
	defer unauthorized.Close()
	p := ProbeHealth(context.Background(), nil, unauthorized.URL, "opencode", "wrong")
	if p.Healthy {
		t.Fatal("401 must not be reported as healthy")
	}
	if p.Status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", p.Status)
	}
	if !strings.Contains(p.Summary(), "401") {
		t.Fatalf("summary = %q, want it to mention 401", p.Summary())
	}
}

func TestProbeHealthConnectionRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening now

	p := ProbeHealth(context.Background(), &http.Client{Timeout: time.Second}, url, "opencode", "pw")
	if p.Healthy || p.Err == nil {
		t.Fatalf("probe = %+v, want a connection error", p)
	}
	if strings.TrimSpace(p.Summary()) == "" {
		t.Fatal("summary should describe the connection failure")
	}
}

// TestWaitHealthyErrorCarriesLastProbe is the regression guard for the reported
// bug: a 401 (wrong password) used to look identical to a dead backend, with no
// output for two minutes and no diagnosis at the end.
func TestWaitHealthyErrorCarriesLastProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("unauthorized"))
	}))
	defer srv.Close()

	cfg := HealthConfig{Deadline: 60 * time.Millisecond, PollInterval: 5 * time.Millisecond, RequestTimeout: time.Second}
	err := WaitHealthy(context.Background(), srv.URL, "opencode", "wrong", cfg, nil)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	for _, want := range []string{"401", "unauthorized", srv.URL} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q should mention %q", err, want)
		}
	}
}

func TestWaitHealthyReportsProgress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("starting"))
	}))
	defer srv.Close()

	var progress bytes.Buffer
	cfg := HealthConfig{
		Deadline:         200 * time.Millisecond,
		PollInterval:     5 * time.Millisecond,
		RequestTimeout:   time.Second,
		Progress:         &progress,
		ProgressInterval: 20 * time.Millisecond,
	}
	if err := WaitHealthy(context.Background(), srv.URL, "opencode", "pw", cfg, nil); err == nil {
		t.Fatal("expected a timeout error")
	}
	out := progress.String()
	if !strings.Contains(out, "still waiting for the backend") {
		t.Fatalf("progress output = %q, want a waiting line", out)
	}
	if !strings.Contains(out, srv.URL) {
		t.Fatalf("progress output = %q, want the endpoint", out)
	}
}

func TestReportRunningHealthDoesNotClaimHealth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("starting"))
	}))
	defer srv.Close()
	// Smoke check: this must not panic and must not hang.
	ReportRunningHealth(context.Background(), RuntimeMicrosandbox, srv.URL, "opencode", "pw")
}

func TestWaitHealthyContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("starting"))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	cfg := HealthConfig{Deadline: 0, PollInterval: 5 * time.Millisecond, RequestTimeout: time.Second}
	if err := WaitHealthy(ctx, srv.URL, "opencode", "pw", cfg, nil); err == nil {
		t.Fatal("expected context cancellation error")
	}
}

// TestCheckBackendLargeProviderPayload is the regression test for the reported
// bug: the /provider response embeds the full models.dev catalog and exceeded
// the 4 MiB read cap, so the JSON was truncated mid-value and check failed with
// a bare "unexpected EOF" after printing the health line.
func TestCheckBackendLargeProviderPayload(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"location":{"directory":"/workspace"},"data":[{"id":"albert","name":"Albert","models":[`)
	for j := 0; j < 400; j++ {
		if j > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id":"model-%d","name":"model-with-a-reasonably-long-name-%d","limit":{"context":131072,"output":65536}}`, j, j)
	}
	b.WriteString(`]}]}`)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/info":
			_, _ = w.Write([]byte(`{"version":"2.2.5","pid":1,"urls":[],"paths":{"tmp":"/tmp/opencode"}}`))
		case "/api/provider":
			_, _ = w.Write([]byte(b.String()))
		}
	}))
	defer srv.Close()

	out := captureStdout(t, func() {
		if err := CheckBackend(context.Background(), nil, srv.URL, "opencode", "pw"); err != nil {
			t.Fatalf("CheckBackend: %v", err)
		}
	})
	if !strings.Contains(out, "albert provider: registered") {
		t.Fatalf("provider line missing from check output: %q", out)
	}
}
