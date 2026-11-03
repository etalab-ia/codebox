package justcode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HealthConfig tunes the backend health wait. Defaults mirror the shell: a
// 120-second wall-clock deadline and a 5-second per-request timeout.
type HealthConfig struct {
	// Quiet hides endpoint and probe detail during the concise launch flow.
	Quiet bool
	// Deadline is the overall wall-clock bound. Zero disables the bound.
	Deadline time.Duration
	// PollInterval is the pause between probes.
	PollInterval time.Duration
	// RequestTimeout bounds each individual request, so a hung connection
	// cannot bypass the wall-clock deadline.
	RequestTimeout time.Duration
	// Progress, when non-nil, receives an occasional status line while waiting
	// so a slow start does not look like a hang.
	Progress io.Writer
	// ProgressInterval is the pause between progress lines. Defaults to 15s.
	ProgressInterval time.Duration
}

// DefaultHealthConfig returns the production health-wait settings.
func DefaultHealthConfig() HealthConfig {
	return HealthConfig{
		Deadline:         120 * time.Second,
		PollInterval:     500 * time.Millisecond,
		RequestTimeout:   5 * time.Second,
		ProgressInterval: 15 * time.Second,
	}
}

// FetchBody performs a basic-authenticated GET to <endpoint><path> and returns
// the response body as a string. An empty password means "no auth".
func FetchBody(ctx context.Context, client *http.Client, endpoint, username, password, path string) (string, error) {
	body, _, err := fetch(ctx, client, endpoint, username, password, path)
	return body, err
}

// maxHTTPBody bounds response reads. The /provider response embeds the full
// models.dev catalog (every provider with its model list), which can exceed
// 4 MiB — the original 4 MiB cap truncated it mid-JSON and surfaced as a bare
// "unexpected EOF" from the decoder. Hitting the cap must produce an explicit
// message, not a truncated stream.
const maxHTTPBody = 64 << 20 // 64 MiB

// readBodyCapped reads up to maxHTTPBody and reports an explicit error rather
// than silently truncating.
func readBodyCapped(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxHTTPBody+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxHTTPBody {
		return nil, fmt.Errorf("response exceeds %d MiB limit", maxHTTPBody>>20)
	}
	return b, nil
}

// fetch returns the body and the HTTP status code.
func fetch(ctx context.Context, client *http.Client, endpoint, username, password, path string) (string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+path, nil)
	if err != nil {
		return "", 0, err
	}
	req.SetBasicAuth(username, password)
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	b, err := readBodyCapped(resp.Body)
	if err != nil {
		return "", resp.StatusCode, err
	}
	return string(b), resp.StatusCode, nil
}

// FetchJSON performs a basic-authenticated GET to <endpoint><path> and decodes
// the JSON response into v.
func FetchJSON(ctx context.Context, client *http.Client, endpoint, username, password, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+path, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(username, password)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s%s returned %s", endpoint, path, resp.Status)
	}
	b, err := readBodyCapped(resp.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// HealthProbe is the outcome of a single health request.
type HealthProbe struct {
	Status  int
	Healthy bool
	Body    string
	Err     error
}

// Summary renders a probe outcome for a diagnostic message.
func (p HealthProbe) Summary() string {
	if p.Err != nil {
		return p.Err.Error()
	}
	if p.Status != 0 && p.Status != http.StatusOK {
		return fmt.Sprintf("HTTP %d: %s", p.Status, truncate(p.Body, 120))
	}
	return fmt.Sprintf("HTTP %d: %s", p.Status, truncate(p.Body, 120))
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ProbeHealth performs one health request against <endpoint>/api/info.
func ProbeHealth(ctx context.Context, client *http.Client, endpoint, username, password string) HealthProbe {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	body, status, err := fetch(ctx, client, endpoint, username, password, "/api/info")
	p := HealthProbe{Status: status, Body: body, Err: err}
	if err == nil && status == http.StatusOK {
		var info struct {
			Version string   `json:"version"`
			PID     *int     `json:"pid"`
			URLs    []string `json:"urls"`
			Paths   struct {
				Tmp string `json:"tmp"`
			} `json:"paths"`
		}
		p.Healthy = json.Unmarshal([]byte(body), &info) == nil &&
			strings.HasPrefix(info.Version, "2.") && info.PID != nil && *info.PID >= 0 &&
			info.URLs != nil && info.Paths.Tmp != ""
	}
	return p
}

// WaitHealthy polls <endpoint>/api/info until the body reports healthy or
// the wall-clock deadline expires. On failure the error carries the last probe
// outcome, so an auth failure, a refused connection, and a still-starting
// backend are distinguishable.
func WaitHealthy(ctx context.Context, endpoint, username, password string, cfg HealthConfig, client *http.Client) error {
	if client == nil {
		client = &http.Client{Timeout: cfg.RequestTimeout}
	}
	if cfg.ProgressInterval <= 0 {
		cfg.ProgressInterval = 15 * time.Second
	}
	var deadline time.Time
	if cfg.Deadline > 0 {
		deadline = time.Now().Add(cfg.Deadline)
	}
	start := time.Now()
	nextProgress := start.Add(cfg.ProgressInterval)

	var last HealthProbe
	for {
		last = ProbeHealth(ctx, client, endpoint, username, password)
		if last.Healthy {
			return nil
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			if cfg.Quiet {
				return fmt.Errorf("l'agent n'est pas prêt après %s ; détail : just-code doctor", cfg.Deadline)
			}
			return fmt.Errorf("backend at %s did not become healthy within %s (last probe: %s)",
				endpoint, cfg.Deadline, last.Summary())
		}
		if !cfg.Quiet && cfg.Progress != nil && time.Now().After(nextProgress) {
			fmt.Fprintf(cfg.Progress, "still waiting for the backend at %s (%s elapsed; last probe: %s)\n",
				endpoint, time.Since(start).Round(time.Second), last.Summary())
			nextProgress = time.Now().Add(cfg.ProgressInterval)
		}
		select {
		case <-time.After(cfg.PollInterval):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

type providerList struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// ReportRunningHealth prints an accurate status line for a runtime that is
// already running. Without this, `start`/`code` would silently report success
// on a sandbox whose backend has died, and the caller would then wait out the
// full health deadline for no reason.
func ReportRunningHealth(ctx context.Context, rt Runtime, endpoint, username, password string) {
	probe := ProbeHealth(ctx, nil, endpoint, username, password)
	if probe.Healthy {
		fmt.Printf("%s is running with a healthy OpenCode backend.\n", rt)
		return
	}
	fmt.Printf("%s is running but the OpenCode backend is not healthy (%s).\n", rt, probe.Summary())
	fmt.Printf("  Run 'just-code logs --%s' to see why, or 'just-code restart --%s' to recreate it.\n", rt, rt)
}

// CheckBackend reports the health response and Albert provider status of a
// backend endpoint, mirroring `just check`.
func CheckBackend(ctx context.Context, client *http.Client, endpoint, username, password string) error {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	body, err := FetchBody(ctx, client, endpoint, username, password, "/api/info")
	if err != nil {
		return err
	}
	fmt.Println(body)

	var pl providerList
	if err := FetchJSON(ctx, client, endpoint, username, password, "/api/provider", &pl); err != nil {
		return err
	}
	registered := false
	for _, p := range pl.Data {
		if p.ID == "albert" {
			registered = true
			break
		}
	}
	if registered {
		fmt.Println("albert provider: registered")
	} else {
		fmt.Println("albert provider: MISSING")
	}
	return nil
}
