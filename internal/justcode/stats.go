package justcode

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// This file implements the opt-in product statistics introduced by issue
// #181. The contract is deliberately narrow:
//
//   - nothing is built or queued unless the user answered "o" (or ran
//     `just-code stats on`); the default is No;
//   - every payload is assembled from typed fields below, never from free
//     text, a path, a repository name, a prompt, a response, a key or an
//     identity;
//   - sending is fire-and-forget: a detached child flushes the queue, so an
//     unreachable endpoint can neither slow nor block the wizard.
//
// The queue is bounded and local: a host that never reaches an endpoint keeps
// at most statsQueueLimit events.

const (
	// StatsFlushCommand is the hidden subcommand that flushes the queue.
	StatsFlushCommand = "__stats-flush"
	// statsQueueLimit bounds the local queue.
	statsQueueLimit = 32
	// StatsSchemaVersion is the wire schema version.
	StatsSchemaVersion = 1
)

// StatsEndpointDefault is the build-time endpoint. It is intentionally empty:
// no endpoint is invented, and a release build may set it with
// -ldflags "-X github.com/etalab-ia/just-code/internal/justcode.StatsEndpointDefault=...".
// JUST_CODE_STATS_URL overrides it for tests and development.
var StatsEndpointDefault = ""

// StatsFlushSpawn is the flusher starter. Tests in other packages replace it
// so they never start a detached copy of the test binary.
var StatsFlushSpawn = SpawnStatsFlush

// statsQueueLockWait bounds how long a queue mutation waits for another
// process; it is a variable so tests can exercise stale-lock recovery fast.
var statsQueueLockWait = 2 * time.Second

// StatsEndpoint resolves the configured endpoint, or "" when sending is
// disabled.
func StatsEndpoint() string {
	if v := strings.TrimSpace(os.Getenv("JUST_CODE_STATS_URL")); v != "" {
		return v
	}
	return strings.TrimSpace(StatsEndpointDefault)
}

// Two wire shapes reach the same endpoint setting, and the endpoint URL itself
// says which one applies. That keeps a single source of truth: a build cannot
// pair a Grist URL with a JSON body by setting two variables inconsistently.
//
//   - a Grist table's records collection posts ONE record per event and needs
//     no credential, because the document is public and its access rules grant
//     Create on that single table;
//   - anything else receives the batched JSON envelope below, which is the
//     contract a team-run relay implements when it exists.
//
// statsGristRecordsURL reports whether the endpoint targets a Grist records
// collection, accepting the optional `/o/<org>` prefix that Grist doc URLs
// carry. Transport security is a separate question, checked once by
// statsEndpointSecure: folding it in here made an insecure Grist URL look like
// "not a Grist endpoint", which selected the batch sender and posted the
// events to that same http:// URL with the wrong envelope.
func statsGristRecordsURL(endpoint string) (string, bool) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return "", false
	}
	segments := strings.Split(strings.Trim(u.Path, "/"), "/")
	// [/o/<org>]/api/docs/<docId>/tables/<tableId>/records
	// Strip the optional organization prefix only when what follows it can
	// still be a records path: an endpoint ending in "/o" or "/o/" would
	// otherwise slice past the end and panic in the detached flusher.
	if len(segments) > 2 && segments[0] == "o" {
		segments = segments[2:]
	}
	if len(segments) != 6 || segments[0] != "api" || segments[1] != "docs" ||
		segments[3] != "tables" || segments[5] != "records" {
		return "", false
	}
	if segments[2] == "" || segments[4] == "" {
		return "", false
	}
	return endpoint, true
}

// statsEndpointSecure reports whether events may be sent to this endpoint at
// all. Statistics are non-nominative but they are still deployment data, and a
// plaintext POST would expose them on the path; loopback is exempt so the
// tests can exercise the real transports over HTTP.
//
// This is checked separately from the wire format and BEFORE either transport
// runs: an insecure endpoint must stop the flush, not fall through to the
// other sender.
func statsEndpointSecure(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		return isLoopbackStatsHost(u.Hostname())
	default:
		return false
	}
}

// isLoopbackStatsHost lets the tests drive the Grist path over plain HTTP
// without weakening the TLS requirement for a real deployment.
func isLoopbackStatsHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	parsed := net.ParseIP(host)
	return parsed != nil && parsed.IsLoopback()
}

// statsHTTPClient is the client both transports use. It enforces the same
// transport rule on every redirect that statsEndpointSecure enforces on the
// configured endpoint: an HTTPS endpoint that answers 307/308 pointing at a
// plaintext non-loopback host would otherwise have http.DefaultClient replay
// the whole event body in the clear, so the check would cover only the first
// hop.
//
// The default redirect budget is kept (Go stops after 10), and any resolution
// error is caught so a redirect to an unparseable URL fails the flush instead
// of writing to an unintended destination.
func statsHTTPClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			if !statsEndpointSecure(req.URL.String()) {
				return fmt.Errorf("refusing redirect to an insecure endpoint: %s", req.URL.Redacted())
			}
			return nil
		},
	}
}

// statsGristFields maps one event onto the Grist row. Column IDs are part of
// the receiving document's contract, so they are spelled here once. `version`,
// `os` and `arch` are duplicated out of the payload so indicators can be built
// from ordinary Grist formula columns.
func statsGristFields(event StatsEvent) (map[string]any, error) {
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return nil, err
	}
	fields := map[string]any{
		"eventId":       event.EventID,
		"at":            event.At,
		"kind":          event.Kind,
		"machineId":     event.MachineID,
		"schemaVersion": event.SchemaVersion,
		"version":       event.Payload.Version,
		"os":            event.Payload.OS,
		"arch":          event.Payload.Arch,
		"payload":       string(payload),
	}
	if event.ProjectID != "" {
		fields["projectId"] = event.ProjectID
	}
	return fields, nil
}

// ReadStatsMachineID returns the local identifier when it exists.
func ReadStatsMachineID(fs FS, stateDir string) string {
	data, err := fs.ReadFile(statsMachineIDPath(stateDir))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// ReadStatsProjectID returns the local project identifier when it exists.
func ReadStatsProjectID(fs FS, stateDir, root string) string {
	canonical, err := filepath.Abs(root)
	if err != nil {
		canonical = root
	}
	sum := sha256.Sum256([]byte(canonical))
	path := filepath.Join(statsDir(stateDir), "projects", hex.EncodeToString(sum[:])[:16])
	data, err := fs.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// SetupStats is the setup payload. Platform kinds and model references are
// non-nominative; a user-supplied platform name is never included.
type SetupStats struct {
	Platforms    []string `json:"platforms"`
	Models       []string `json:"models"`
	DefaultModel string   `json:"defaultModel,omitempty"`
	GitHubToken  bool     `json:"githubToken"`
	GitConfig    bool     `json:"gitConfig"`
}

// InitStats is the init payload. Skills and MCPs are 0/1 maps keyed by
// catalogue ID.
type InitStats struct {
	Skills        map[string]bool `json:"skills,omitempty"`
	MCPs          map[string]bool `json:"mcps,omitempty"`
	GitHubCase    int             `json:"githubCase,omitempty"`
	Advanced      bool            `json:"advanced"`
	Harness       string          `json:"harness"`
	Runtime       string          `json:"runtime"`
	Isolation     string          `json:"isolation"`
	SkillsStorage string          `json:"skillsStorage"`
}

// AbandonStats records where a user left the wizard.
type AbandonStats struct {
	Stage string `json:"stage"`
}

// StatsPayload is the non-nominative configuration summary.
type StatsPayload struct {
	Version string        `json:"version,omitempty"`
	OS      string        `json:"os"`
	Arch    string        `json:"arch"`
	Setup   *SetupStats   `json:"setup,omitempty"`
	Init    *InitStats    `json:"init,omitempty"`
	Abandon *AbandonStats `json:"abandon,omitempty"`
}

// StatsEvent is one queued event.
type StatsEvent struct {
	SchemaVersion int          `json:"schemaVersion"`
	EventID       string       `json:"eventId"`
	At            string       `json:"at"`
	MachineID     string       `json:"machineId"`
	ProjectID     string       `json:"projectId,omitempty"`
	Kind          string       `json:"kind"`
	Payload       StatsPayload `json:"payload"`
}

// StatsEventInput describes one event to build.
type StatsEventInput struct {
	Kind    string // setup | init | abandon
	Stage   string
	Version string
	// ProjectRoot is used only to derive a local project identifier. It is
	// never serialised.
	ProjectRoot string
	Setup       *SetupStats
	Init        *InitStats
}

// BuildStatsEvent assembles an event without any free-form input.
func BuildStatsEvent(fs FS, stateDir string, in StatsEventInput) (StatsEvent, error) {
	machine, err := statsMachineID(fs, stateDir)
	if err != nil {
		return StatsEvent{}, err
	}
	event := StatsEvent{
		SchemaVersion: StatsSchemaVersion,
		EventID:       statsRandomID(),
		At:            time.Now().UTC().Format(time.RFC3339),
		MachineID:     machine,
		Kind:          in.Kind,
		Payload: StatsPayload{
			Version: in.Version,
			OS:      runtime.GOOS,
			Arch:    runtime.GOARCH,
			Setup:   sanitizeSetupStats(in.Setup),
			Init:    sanitizeInitStats(in.Init),
		},
	}
	if in.Kind == "abandon" {
		event.Payload.Abandon = &AbandonStats{Stage: in.Stage}
	}
	if strings.TrimSpace(in.ProjectRoot) != "" {
		project, perr := statsProjectID(fs, stateDir, in.ProjectRoot)
		if perr != nil {
			return StatsEvent{}, perr
		}
		event.ProjectID = project
	}
	return event, nil
}

// sanitizeSetupStats keeps only the documented fields and copies them.
func sanitizeSetupStats(in *SetupStats) *SetupStats {
	if in == nil {
		return nil
	}
	out := &SetupStats{
		Platforms:    append([]string(nil), in.Platforms...),
		Models:       append([]string(nil), in.Models...),
		DefaultModel: in.DefaultModel,
		GitHubToken:  in.GitHubToken,
		GitConfig:    in.GitConfig,
	}
	sort.Strings(out.Platforms)
	sort.Strings(out.Models)
	return out
}

func sanitizeInitStats(in *InitStats) *InitStats {
	if in == nil {
		return nil
	}
	out := &InitStats{
		Skills:        copyBoolMap(in.Skills),
		MCPs:          copyBoolMap(in.MCPs),
		GitHubCase:    in.GitHubCase,
		Advanced:      in.Advanced,
		Harness:       in.Harness,
		Runtime:       in.Runtime,
		Isolation:     in.Isolation,
		SkillsStorage: in.SkillsStorage,
	}
	return out
}

func copyBoolMap(in map[string]bool) map[string]bool {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]bool, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// statsDir is the host-local statistics directory.
func statsDir(stateDir string) string { return filepath.Join(stateDir, "stats") }

func statsQueuePath(stateDir string) string { return filepath.Join(statsDir(stateDir), "queue.json") }

func statsMachineIDPath(stateDir string) string {
	return filepath.Join(statsDir(stateDir), "machine-id")
}

// statsRandomID returns 16 random bytes as hex. It is never derived from
// host or user data.
func statsRandomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

// statsMachineID returns the local random identifier, creating it on first
// use with owner-only permissions. Creation is exclusive: two processes
// starting together must end up sharing ONE identifier, so the loser reads
// back the winner's value instead of writing its own.
func statsMachineID(fs FS, stateDir string) (string, error) {
	path := statsMachineIDPath(stateDir)
	if id := readStatsIDFile(fs, path); id != "" {
		return id, nil
	}
	id := statsRandomID()
	if id == "" {
		return "", fmt.Errorf("cannot generate the local statistics identifier")
	}
	if err := fs.MkdirAll(statsDir(stateDir), 0o700); err != nil {
		return "", err
	}
	if err := fs.CreateExclusive(path, []byte(id+"\n"), 0o600); err != nil {
		if errors.Is(err, os.ErrExist) {
			if winner := readStatsIDFile(fs, path); winner != "" {
				return winner, nil
			}
		}
		return "", err
	}
	return id, nil
}

// readStatsIDFile reads an identifier file, returning "" when absent.
func readStatsIDFile(fs FS, path string) string {
	data, err := fs.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// statsProjectID returns a random per-project identifier. The project is
// identified by the SHA-256 of its canonical root, which is only used as a
// local filename: the root itself never leaves the host.
func statsProjectID(fs FS, stateDir, root string) (string, error) {
	canonical, err := filepath.Abs(root)
	if err != nil {
		canonical = root
	}
	sum := sha256.Sum256([]byte(canonical))
	path := filepath.Join(statsDir(stateDir), "projects", hex.EncodeToString(sum[:])[:16])
	if id := readStatsIDFile(fs, path); id != "" {
		return id, nil
	}
	id := statsRandomID()
	if id == "" {
		return "", fmt.Errorf("cannot generate the project statistics identifier")
	}
	if err := fs.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := fs.CreateExclusive(path, []byte(id+"\n"), 0o600); err != nil {
		if errors.Is(err, os.ErrExist) {
			if winner := readStatsIDFile(fs, path); winner != "" {
				return winner, nil
			}
		}
		return "", err
	}
	return id, nil
}

// EnqueueStatsEvent appends one event, keeping the queue bounded. Callers
// must check StatsEnabled first.
func EnqueueStatsEvent(fs FS, stateDir string, event StatsEvent) error {
	return withStatsQueueLock(fs, stateDir, func() error {
		// Recheck consent inside the critical section: a withdrawal that
		// completed while this event was being built must not be undone by
		// re-populating the queue it just cleared. An answered journal
		// decision takes precedence, in both directions.
		if j, jerr := ReadSetupJournal(fs, stateDir); jerr == nil && j.StatsConsentAsked && !j.StatsEnabled {
			return nil
		}
		if settingsPath, err := UserSettingsPath(); err == nil && !StatsEnabled(fs, settingsPath) {
			if j, jerr := ReadSetupJournal(fs, stateDir); jerr != nil || !j.StatsConsentAsked || !j.StatsEnabled {
				return nil
			}
		}
		events, err := PendingStatsEvents(fs, stateDir)
		if err != nil {
			return err
		}
		events = append(events, event)
		if len(events) > statsQueueLimit {
			events = events[len(events)-statsQueueLimit:]
		}
		return writeStatsQueue(fs, stateDir, events)
	})
}

// PendingStatsEvents reads the local queue. A missing file is empty.
func PendingStatsEvents(fs FS, stateDir string) ([]StatsEvent, error) {
	data, err := fs.ReadFile(statsQueuePath(stateDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var events []StatsEvent
	if err := json.Unmarshal(data, &events); err != nil {
		return nil, fmt.Errorf("parse statistics queue: %w", err)
	}
	return events, nil
}

// ClearStatsQueue removes every pending event. `stats off` calls it so the
// next flush has nothing to send.
func ClearStatsQueue(fs FS, stateDir string) error {
	return withStatsQueueLock(fs, stateDir, func() error {
		return writeStatsQueue(fs, stateDir, nil)
	})
}

// writeStatsQueue replaces the queue; an empty set removes the file.
func writeStatsQueue(fs FS, stateDir string, events []StatsEvent) error {
	if len(events) == 0 {
		err := fs.Remove(statsQueuePath(stateDir))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	data, err := json.MarshalIndent(events, "", "  ")
	if err != nil {
		return err
	}
	if err := fs.MkdirAll(statsDir(stateDir), 0o700); err != nil {
		return err
	}
	return atomicWrite(fs, statsQueuePath(stateDir), append(data, '\n'), 0o600)
}

// withStatsQueueLock serialises queue mutations across processes. The lock is
// a create-exclusive file: the OS guarantees a single creator. A stale lock
// (a process killed mid-write) is removed once after a short wait.
func withStatsQueueLock(fs FS, stateDir string, fn func() error) error {
	if err := fs.MkdirAll(statsDir(stateDir), 0o700); err != nil {
		return err
	}
	path := filepath.Join(statsDir(stateDir), "queue.lock")
	deadline := time.Now().Add(statsQueueLockWait)
	recovered := false
	for attempt := 0; ; attempt++ {
		err := fs.CreateExclusive(path, []byte("lock"), 0o600)
		if err == nil {
			defer func() { _ = fs.Remove(path) }()
			return fn()
		}
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		if time.Now().After(deadline) {
			if recovered {
				return err
			}
			// Queue mutations are milliseconds; a lock older than the wait
			// belongs to a process that died while holding it.
			_ = fs.Remove(path)
			recovered = true
			continue
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// removeFlushedStatsEvents drops the events the endpoint acknowledged, while
// preserving anything another process queued during the request.
func removeFlushedStatsEvents(fs FS, stateDir string, sent map[string]bool) error {
	return withStatsQueueLock(fs, stateDir, func() error {
		events, err := PendingStatsEvents(fs, stateDir)
		if err != nil {
			return err
		}
		remaining := events[:0]
		for _, event := range events {
			if !sent[event.EventID] {
				remaining = append(remaining, event)
			}
		}
		return writeStatsQueue(fs, stateDir, remaining)
	})
}

// statsConsentForFlush resolves whether a flush may send. An answered
// journal decision takes precedence, in BOTH directions: a pending refusal
// overrides a durable acceptance, and a pending acceptance is allowed before
// SaveSettings. Otherwise the durable decision applies.
func statsConsentForFlush(fs FS, stateDir, settingsPath string) bool {
	if j, err := ReadSetupJournal(fs, stateDir); err == nil && j.StatsConsentAsked {
		return j.StatsEnabled
	}
	return StatsEnabled(fs, settingsPath)
}

// FlushStats posts every queued event and removes the ones accepted. It is
// silent by design: callers ignore its error (the queue survives).
func FlushStats(ctx context.Context, fs FS, stateDir string) error {
	settingsPath, err := UserSettingsPath()
	if err != nil || !statsConsentForFlush(fs, stateDir, settingsPath) {
		// Consent was withdrawn between enqueue and flush: send nothing.
		return nil
	}
	endpoint := StatsEndpoint()
	if endpoint == "" {
		return nil
	}
	// Transport security is decided once, before the format: an insecure
	// endpoint must stop the flush outright. Letting it fall through to the
	// other transport would still send the events over the same plaintext URL.
	if !statsEndpointSecure(endpoint) {
		return fmt.Errorf("statistics endpoint is not secure: %s", endpoint)
	}
	events, err := PendingStatsEvents(fs, stateDir)
	if err != nil || len(events) == 0 {
		return err
	}
	// Re-check immediately before the request: `stats off` must stop an
	// in-flight flush as early as the process boundary allows.
	if !statsConsentForFlush(fs, stateDir, settingsPath) {
		return nil
	}
	if _, ok := statsGristRecordsURL(endpoint); ok {
		return flushStatsToGrist(ctx, endpoint, fs, stateDir, events)
	}
	body, err := json.Marshal(map[string]any{"schemaVersion": StatsSchemaVersion, "events": events})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := statsHTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("statistics endpoint returned HTTP %d", resp.StatusCode)
	}
	sent := make(map[string]bool, len(events))
	for _, event := range events {
		sent[event.EventID] = true
	}
	return removeFlushedStatsEvents(fs, stateDir, sent)
}

// statsGristMaxPayload bounds the JSON text stored in a single Grist row. The
// largest real event is an init with every skill and MCP flagged, far below
// this; the limit exists so a malformed event cannot post an unbounded cell.
const statsGristMaxPayload = 8 << 10

// statsGristTimeout is per event: Grist answers a single-record create quickly,
// and a stuck write must not hold the detached flusher open.
const statsGristTimeout = 5 * time.Second

// flushStatsToGrist posts each event as one record. An event leaves the queue
// only once Grist has acknowledged it, so an interrupted run retries exactly
// what is still missing.
//
// The receiving table must be reachable without a credential: the document is
// public and its access rules grant Create on that table alone. Because the key
// belongs to no user, it cannot be scoped or leaked, and it is not sent at all.
//
// Retries are not deduplicated server-side. Grist's own recipe for that is an
// access rule on a formula column that counts records sharing the eventId and
// denies Create when the count exceeds one, which rejects the duplicate rather
// than merging it. Grist reports that refusal as 403 with an ACL_DENY message,
// so the flusher treats exactly that response as "already recorded" and leaves
// every other 403 retryable.
func flushStatsToGrist(ctx context.Context, endpoint string, fs FS, stateDir string, events []StatsEvent) error {
	settingsPath, err := UserSettingsPath()
	if err != nil {
		return err
	}
	var firstErr error
	for _, event := range events {
		// Consent is re-read before EVERY request, not once for the batch.
		// `stats off` persists the refusal and clears the queue, but neither
		// action reaches this in-memory snapshot; without this check a
		// withdrawal mid-flush would still send up to every queued event.
		if !statsConsentForFlush(fs, stateDir, settingsPath) {
			return firstErr
		}
		fields, err := statsGristFields(event)
		if err != nil {
			// An event that cannot be encoded will never encode; keeping it
			// would block every later event behind it.
			if err := removeFlushedStatsEvents(fs, stateDir, map[string]bool{event.EventID: true}); err != nil && firstErr == nil {
				firstErr = err
			}
			continue
		}
		if payload, ok := fields["payload"].(string); ok && len(payload) > statsGristMaxPayload {
			if err := removeFlushedStatsEvents(fs, stateDir, map[string]bool{event.EventID: true}); err != nil && firstErr == nil {
				firstErr = err
			}
			continue
		}
		body, err := json.Marshal(map[string]any{"records": []any{map[string]any{"fields": fields}}})
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		status, respBody, err := postStatsGristRecord(ctx, endpoint, body)
		switch {
		case err != nil:
			// The endpoint is unreachable or the request failed: stop, keep
			// everything, and retry the same set on the next flush.
			return err
		case status >= 200 && status < 300:
		case status == http.StatusForbidden && statsGristDuplicateRejection(respBody):
			// The duplicate guard refused the write, so the event is already
			// stored and its response was merely lost. Retrying would keep it
			// in the queue until eviction.
		default:
			// Everything else keeps the event and is reported. In particular a
			// 400 is NOT an event-level rejection: Grist returns it when the
			// request does not match the table ("Invalid column", "Can't save
			// value to formula column"), which is a configuration error that
			// affects every event equally. Discarding on it would destroy the
			// whole dataset while reporting nothing, and the events become
			// deliverable again as soon as the table is corrected.
			if firstErr == nil {
				firstErr = fmt.Errorf("statistics endpoint returned HTTP %d: %s", status, statsGristErrorSummary(respBody))
			}
			continue
		}
		if err := removeFlushedStatsEvents(fs, stateDir, map[string]bool{event.EventID: true}); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// statsGristDuplicateRejection reports whether a response is the duplicate
// guard refusing a write, which means the event is already stored.
//
// Grist names the rule type that denied the action in its error message, which
// is what separates "already stored" from "not writable right now":
//
//	Blocked by row create access rules    <- a row-level rule (newRec.Count > 1)
//	Blocked by table create access rules  <- a table-wide or default rule
//
// The duplicate guard is necessarily row-level, because it inspects newRec,
// while a misconfigured or temporarily withdrawn permission is table-level.
// Verified against grist.numerique.gouv.fr: the guard returns the first form
// and every configuration denial the second.
//
// Deliberately NOT keyed on details.memos: Grist documents a memo on a rule
// condition, but this server returns an empty memos array for every denial, so
// requiring one would make the duplicate path dead code.
func statsGristDuplicateRejection(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	var parsed struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return false
	}
	return strings.Contains(parsed.Error, "Blocked by row ") &&
		strings.Contains(parsed.Error, "access rules")
}

// postStatsGristRecord sends one record and reports the status code and body.
// The body is read (and therefore drained) so a keep-alive connection can be
// reused; the caller needs the tail of it to classify a rejection.
func postStatsGristRecord(ctx context.Context, endpoint string, body []byte) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, statsGristTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := statsHTTPClient().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, statsGristMaxErrorBody))
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, respBody, nil
}

// statsGristMaxErrorBody bounds how much of an error response is inspected.
const statsGristMaxErrorBody = 4 << 10

// statsGristErrorSummary extracts Grist's own error text so a failed flush says
// what to fix ("Invalid column \"x\"") instead of only the status code.
func statsGristErrorSummary(body []byte) string {
	if len(body) == 0 {
		return "no response body"
	}
	var parsed struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Error != "" {
		// Grist prefixes engine errors; keep the whole message but bound it, as
		// it can embed a traceback.
		message := parsed.Error
		if len(message) > 200 {
			message = message[:200] + "…"
		}
		return message
	}
	return string(body)
}

// SpawnStatsFlush starts the detached flusher and returns immediately. The
// caller never waits: an unreachable endpoint must not slow the wizard.
func SpawnStatsFlush() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(exe, StatsFlushCommand)
	detachProcess(cmd)
	if err := cmd.Start(); err != nil {
		return
	}
	_ = cmd.Process.Release()
}

// ReadStatsConsent returns the durable consent state. A missing settings file
// is "not asked, disabled".
func ReadStatsConsent(fs FS, path string) (enabled, asked bool) {
	us, err := ReadUserSettings(fs, path)
	if err != nil {
		return false, false
	}
	return us.StatsEnabled, us.StatsConsentAsked
}

// WriteStatsConsent persists the consent decision. Enabling is always an
// explicit answer, so it also marks the question as asked.
func WriteStatsConsent(fs FS, path string, enabled, asked bool) error {
	us, err := ReadUserSettings(fs, path)
	if err != nil {
		return err
	}
	us.StatsEnabled = enabled
	us.StatsConsentAsked = asked || enabled
	return WriteUserSettings(fs, path, us)
}

// StatsEnabled reports whether this host opted in.
func StatsEnabled(fs FS, path string) bool {
	enabled, _ := ReadStatsConsent(fs, path)
	return enabled
}

// RecordStatsEvent is the guarded entry point: it does nothing unless the
// host opted in, and it never returns an error to the caller (statistics must
// never break a wizard). When the event is queued, a detached flush starts.
//
// An abandonment event is additionally gated on the consent question having
// been ANSWERED already. Consent is asked at the end of setup, so recording a
// quit before that screen would ask about a run that never reached the
// question, exactly what the "asked once, at the end" rule forbids. A host
// that already answered (including opted out) keeps its normal behaviour.
func RecordStatsEvent(fs FS, stateDir string, in StatsEventInput) {
	path, err := UserSettingsPath()
	if err != nil {
		return
	}
	enabled, asked := ReadStatsConsent(fs, path)
	// A pending setup decision is newer than the durable setting, for EVERY
	// event kind: an init that follows an abandoned setup must not be dropped
	// after a pending acceptance, nor queued after a pending refusal.
	if j, jerr := ReadSetupJournal(fs, stateDir); jerr == nil && j.StatsConsentAsked {
		enabled, asked = j.StatsEnabled, true
	}
	if !enabled || (in.Kind == "abandon" && !asked) {
		return
	}
	event, err := BuildStatsEvent(fs, stateDir, in)
	if err != nil {
		return
	}
	if err := EnqueueStatsEvent(fs, stateDir, event); err != nil {
		return
	}
	StatsFlushSpawn()
}
