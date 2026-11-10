package justcode

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// gristTestPath is the records collection shape the transport recognises.
const gristTestPath = "/api/docs/DOC123/tables/Events/records"

func TestStatsGristRecordsURLRecognisesOnlyARecordsCollection(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		want     bool
	}{
		{"records collection", "https://grist.example.fr" + gristTestPath, true},
		{"org prefix", "https://grist.example.fr/o/albert" + gristTestPath, true},
		{"trailing slash", "https://grist.example.fr" + gristTestPath + "/", true},
		{"plain endpoint", "https://stats.example.fr/v1/events", false},
		{"grist table, not records", "https://grist.example.fr/api/docs/DOC123/tables/Events", false},
		{"missing table", "https://grist.example.fr/api/docs/DOC123/tables//records", false},
		{"http is refused", "http://grist.example.fr" + gristTestPath, false},
		{"no scheme", "grist.example.fr" + gristTestPath, false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, got := statsGristRecordsURL(tc.endpoint); got != tc.want {
				t.Fatalf("statsGristRecordsURL(%q) = %v, want %v", tc.endpoint, got, tc.want)
			}
		})
	}
}

// The TLS rule must not make the Grist path untestable: a loopback test server
// is plain HTTP and still has to be recognised.
func TestStatsGristRecordsURLAcceptsLoopbackOverHTTP(t *testing.T) {
	if _, ok := statsGristRecordsURL("http://127.0.0.1:8080" + gristTestPath); !ok {
		t.Fatal("a loopback Grist endpoint over HTTP must be recognised")
	}
	if _, ok := statsGristRecordsURL("http://localhost:8080" + gristTestPath); !ok {
		t.Fatal("localhost must be treated as loopback")
	}
}

// A path ending at the organization prefix has nothing left to parse. Every
// nonempty queue reaches this helper, so a panic here kills the detached
// flusher instead of simply classifying the endpoint as non-Grist.
func TestStatsGristRecordsURLDoesNotPanicOnATruncatedPath(t *testing.T) {
	for _, endpoint := range []string{
		"https://grist.example.fr/o",
		"https://grist.example.fr/o/",
		"https://grist.example.fr/api",
		"https://grist.example.fr/api/docs",
	} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("statsGristRecordsURL(%q) panicked: %v", endpoint, r)
				}
			}()
			if _, ok := statsGristRecordsURL(endpoint); ok {
				t.Fatalf("statsGristRecordsURL(%q) must not be a records collection", endpoint)
			}
		}()
	}
}

func TestStatsGristFieldsCarryTheDocumentedColumns(t *testing.T) {
	event := StatsEvent{
		SchemaVersion: 1,
		EventID:       "evt-1",
		At:            "2026-11-10T14:00:00Z",
		MachineID:     "machine-1",
		ProjectID:     "project-1",
		Kind:          "init",
		Payload: StatsPayload{
			Version: "0.16.0",
			OS:      "linux",
			Arch:    "arm64",
			Init:    &InitStats{Harness: "opencode"},
		},
	}
	fields, err := statsGristFields(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"eventId", "at", "kind", "machineId", "schemaVersion", "version", "os", "arch", "payload", "projectId"} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("missing column %q in %v", key, fields)
		}
	}
	// Version, os and arch are duplicated out of the payload so indicators can
	// be formula columns; they must agree with what the payload says.
	if fields["version"] != "0.16.0" || fields["os"] != "linux" || fields["arch"] != "arm64" {
		t.Fatalf("denormalised columns disagree with the payload: %v", fields)
	}
	// The payload stays a JSON string: parsing it into the row would require a
	// schema change for every new field.
	var decoded StatsPayload
	if err := json.Unmarshal([]byte(fields["payload"].(string)), &decoded); err != nil {
		t.Fatalf("payload must be JSON text: %v", err)
	}
	if decoded.Init == nil || decoded.Init.Harness != "opencode" {
		t.Fatalf("payload lost the init detail: %+v", decoded)
	}
}

// A setup event has no project, and an absent project must not be sent as an
// empty string: Grist would store it as a real value.
func TestStatsGristFieldsOmitAnAbsentProject(t *testing.T) {
	fields, err := statsGristFields(StatsEvent{SchemaVersion: 1, EventID: "e", Kind: "setup"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["projectId"]; ok {
		t.Fatalf("an absent project must not be sent: %v", fields)
	}
}

func TestFlushStatsToGristPostsOneRecordPerEventWithoutACredential(t *testing.T) {
	statsTestHome(t)
	if err := WriteStatsConsent(DefaultFS, mustSettingsPath(t), true, true); err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	for _, id := range []string{"evt-1", "evt-2"} {
		if err := EnqueueStatsEvent(DefaultFS, stateDir, StatsEvent{
			SchemaVersion: 1, EventID: id, Kind: "setup",
			Payload: StatsPayload{Version: "0.16.0", OS: "linux", Arch: "arm64"},
		}); err != nil {
			t.Fatal(err)
		}
	}

	var mu sync.Mutex
	var bodies []map[string]any
	var sawAuthorization bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != gristTestPath {
			t.Errorf("path = %q, want %q", r.URL.Path, gristTestPath)
		}
		if auth := r.Header.Get("Authorization"); auth != "" {
			mu.Lock()
			sawAuthorization = true
			mu.Unlock()
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("body is not JSON: %v", err)
		}
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"records":[{"id":1}]}`))
	}))
	defer server.Close()

	t.Setenv("JUST_CODE_STATS_URL", server.URL+gristTestPath)
	if err := FlushStats(context.Background(), DefaultFS, stateDir); err != nil {
		t.Fatal(err)
	}

	if sawAuthorization {
		t.Fatal("the Grist transport must send no credential: a user-owned key cannot be scoped")
	}
	if len(bodies) != 2 {
		t.Fatalf("posted %d records, want one per event (2)", len(bodies))
	}
	for _, body := range bodies {
		// One record per request, in Grist's own envelope: {"records":[{fields:…}]}
		records, ok := body["records"].([]any)
		if !ok || len(records) != 1 {
			t.Fatalf("body must carry exactly one record: %v", body)
		}
		record, ok := records[0].(map[string]any)
		if !ok {
			t.Fatalf("record is not an object: %v", records[0])
		}
		if _, ok := record["fields"].(map[string]any); !ok {
			t.Fatalf("record must carry fields: %v", record)
		}
		if _, ok := body["events"]; ok {
			t.Fatal("the Grist transport must not send the batched JSON envelope")
		}
	}
	if events, _ := PendingStatsEvents(DefaultFS, stateDir); len(events) != 0 {
		t.Fatalf("an acknowledged flush must clear the queue: %v", events)
	}
}

// The documented duplicate guard (newRec.Count > 1) rejects the write through
// access rules. Grist answers 403 with an ACL_DENY message, so this uses the
// real response shape: a synthetic 409 would have passed without covering it.
func TestFlushStatsToGristDropsAnEventTheAccessRulesReject(t *testing.T) {
	statsTestHome(t)
	if err := WriteStatsConsent(DefaultFS, mustSettingsPath(t), true, true); err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	if err := EnqueueStatsEvent(DefaultFS, stateDir, StatsEvent{SchemaVersion: 1, EventID: "dupe", Kind: "setup"}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"Blocked by table Create access rules","memos":{}}`))
	}))
	defer server.Close()
	t.Setenv("JUST_CODE_STATS_URL", server.URL+gristTestPath)

	if err := FlushStats(context.Background(), DefaultFS, stateDir); err != nil {
		t.Fatalf("a rejected record is not a transport failure: %v", err)
	}
	if events, _ := PendingStatsEvents(DefaultFS, stateDir); len(events) != 0 {
		t.Fatalf("a rejected event must not stay queued: %v", events)
	}
}

// A 403 that is not an access-rule rejection must stay queued: an operator who
// fixes the rules should still see the backlog delivered.
func TestFlushStatsToGristKeepsAnUnrelated403(t *testing.T) {
	statsTestHome(t)
	if err := WriteStatsConsent(DefaultFS, mustSettingsPath(t), true, true); err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	if err := EnqueueStatsEvent(DefaultFS, stateDir, StatsEvent{SchemaVersion: 1, EventID: "e", Kind: "setup"}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"Unauthorized"}`))
	}))
	defer server.Close()
	t.Setenv("JUST_CODE_STATS_URL", server.URL+gristTestPath)

	if err := FlushStats(context.Background(), DefaultFS, stateDir); err == nil {
		t.Fatal("an unrelated 403 must be reported")
	}
	if events, _ := PendingStatsEvents(DefaultFS, stateDir); len(events) != 1 {
		t.Fatalf("an unrelated 403 must keep the event: %v", events)
	}
}

// `stats off` during a flush must stop it. The loop holds an in-memory snapshot
// the command cannot reach, so consent has to be re-read per request.
func TestFlushStatsToGristStopsWhenConsentIsWithdrawnMidFlush(t *testing.T) {
	statsTestHome(t)
	if err := WriteStatsConsent(DefaultFS, mustSettingsPath(t), true, true); err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	for _, id := range []string{"evt-1", "evt-2", "evt-3", "evt-4"} {
		if err := EnqueueStatsEvent(DefaultFS, stateDir, StatsEvent{SchemaVersion: 1, EventID: id, Kind: "setup"}); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		if first {
			// The user runs `stats off` while this first request is in flight.
			if err := WriteStatsConsent(DefaultFS, mustSettingsPath(t), false, true); err != nil {
				t.Errorf("withdraw consent: %v", err)
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	t.Setenv("JUST_CODE_STATS_URL", server.URL+gristTestPath)

	if err := FlushStats(context.Background(), DefaultFS, stateDir); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 1 {
		t.Fatalf("no request may follow a withdrawal, made %d", got)
	}
}

// An unreachable endpoint keeps everything: this is the offline queue the issue
// asks to test, reached through the Grist transport rather than the batch one.
func TestFlushStatsToGristKeepsEventsWhenTheEndpointIsUnreachable(t *testing.T) {
	statsTestHome(t)
	if err := WriteStatsConsent(DefaultFS, mustSettingsPath(t), true, true); err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	for _, id := range []string{"a", "b"} {
		if err := EnqueueStatsEvent(DefaultFS, stateDir, StatsEvent{SchemaVersion: 1, EventID: id, Kind: "setup"}); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	endpoint := server.URL + gristTestPath
	server.Close()
	t.Setenv("JUST_CODE_STATS_URL", endpoint)

	if err := FlushStats(context.Background(), DefaultFS, stateDir); err == nil {
		t.Fatal("an unreachable endpoint must be reported")
	}
	if events, _ := PendingStatsEvents(DefaultFS, stateDir); len(events) != 2 {
		t.Fatalf("an unreachable endpoint must keep every event: %v", events)
	}
}

// A 5xx is per event, not per flush: the healthy events around it must still be
// delivered and dequeued.
func TestFlushStatsToGristKeepsGoingAfterAServerError(t *testing.T) {
	statsTestHome(t)
	if err := WriteStatsConsent(DefaultFS, mustSettingsPath(t), true, true); err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	for _, id := range []string{"ok-1", "boom", "ok-2"} {
		if err := EnqueueStatsEvent(DefaultFS, stateDir, StatsEvent{SchemaVersion: 1, EventID: id, Kind: "setup"}); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Records []struct {
				Fields struct {
					EventID string `json:"eventId"`
				} `json:"fields"`
			} `json:"records"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("bad body: %v", err)
		}
		id := body.Records[0].Fields.EventID
		mu.Lock()
		seen = append(seen, id)
		mu.Unlock()
		if id == "boom" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	t.Setenv("JUST_CODE_STATS_URL", server.URL+gristTestPath)

	err := FlushStats(context.Background(), DefaultFS, stateDir)
	if err == nil {
		t.Fatal("a 5xx must be reported")
	}
	if len(seen) != 3 {
		t.Fatalf("every queued event must be attempted, saw %v", seen)
	}
	events, _ := PendingStatsEvents(DefaultFS, stateDir)
	if len(events) != 1 || events[0].EventID != "boom" {
		t.Fatalf("only the failing event may stay queued: %+v", events)
	}
}

// An oversized event cannot be posted and must not block the ones behind it.
func TestFlushStatsToGristDropsAnOversizedPayload(t *testing.T) {
	statsTestHome(t)
	if err := WriteStatsConsent(DefaultFS, mustSettingsPath(t), true, true); err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	huge := StatsEvent{
		SchemaVersion: 1, EventID: "huge", Kind: "init",
		Payload: StatsPayload{Init: &InitStats{Skills: map[string]bool{strings.Repeat("s", statsGristMaxPayload): true}}},
	}
	if err := EnqueueStatsEvent(DefaultFS, stateDir, huge); err != nil {
		t.Fatal(err)
	}
	if err := EnqueueStatsEvent(DefaultFS, stateDir, StatsEvent{SchemaVersion: 1, EventID: "small", Kind: "setup"}); err != nil {
		t.Fatal(err)
	}
	var posted []string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), "ssssssssssss") {
			t.Errorf("an oversized payload must not be posted")
		}
		mu.Lock()
		posted = append(posted, string(raw))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	t.Setenv("JUST_CODE_STATS_URL", server.URL+gristTestPath)

	if err := FlushStats(context.Background(), DefaultFS, stateDir); err != nil {
		t.Fatal(err)
	}
	if len(posted) != 1 {
		t.Fatalf("only the small event may be posted, got %d", len(posted))
	}
	if events, _ := PendingStatsEvents(DefaultFS, stateDir); len(events) != 0 {
		t.Fatalf("an unpostable event must not stay queued: %v", events)
	}
}

// Withdrawal must also stop the Grist transport, not just the batched one.
func TestFlushStatsToGristRespectsWithdrawnConsent(t *testing.T) {
	statsTestHome(t)
	if err := WriteStatsConsent(DefaultFS, mustSettingsPath(t), false, true); err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	if err := EnqueueStatsEvent(DefaultFS, stateDir, StatsEvent{SchemaVersion: 1, EventID: "e", Kind: "setup"}); err != nil {
		t.Fatal(err)
	}
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
	}))
	defer server.Close()
	t.Setenv("JUST_CODE_STATS_URL", server.URL+gristTestPath)

	if err := FlushStats(context.Background(), DefaultFS, stateDir); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("nothing may be sent after `stats off`, calls = %d", calls)
	}
}
