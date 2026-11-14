package gateway

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// scalewayFake models the parts of Secret Manager the adapter uses, including
// monotonic revisions and per-version enabled state, so the compare-and-swap
// path is exercised for real.
type scalewayFake struct {
	mu       sync.Mutex
	versions map[string][]fakeVersion // key: path+"/"+name
	sids     map[string]string
	nextID   int
	// beforeAppend lets a test interleave a competing write between this
	// writer's revision check and its append.
	beforeAppend func()
	writes       int
}

type fakeVersion struct {
	revision uint32
	data     []byte
	enabled  bool
}

func newScalewayFake(t *testing.T) (*scalewayFake, *httptest.Server) {
	t.Helper()
	f := &scalewayFake{versions: map[string][]fakeVersion{}, sids: map[string]string{}}
	mux := http.NewServeMux()

	mux.HandleFunc("/secret-manager/v1beta1/regions/fr-par/secrets", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			f.mu.Lock()
			name, path := r.URL.Query().Get("name"), r.URL.Query().Get("path")
			id, ok := f.sids[path+"/"+name]
			f.mu.Unlock()
			if !ok {
				_ = json.NewEncoder(w).Encode(map[string]any{"secrets": []any{}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"secrets": []map[string]string{{"id": id, "path": path, "name": name}},
			})
		case http.MethodPost:
			var body struct {
				Name, Path string
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.nextID++
			id := fmt.Sprintf("00000000-0000-4000-8000-%012d", f.nextID)
			f.sids[body.Path+"/"+body.Name] = id
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "protected": true})
		}
	})

	mux.HandleFunc("/secret-manager/v1beta1/regions/fr-par/secrets/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/secret-manager/v1beta1/regions/fr-par/secrets/"), "/")
		if len(parts) < 2 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		secretID := parts[0]
		f.mu.Lock()
		key := ""
		for k, v := range f.sids {
			if v == secretID {
				key = k
			}
		}
		f.mu.Unlock()

		switch {
		case len(parts) == 2 && parts[1] == "versions" && r.Method == http.MethodPost:
			if f.beforeAppend != nil {
				f.beforeAppend()
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			var body struct {
				Data            string `json:"data"`
				DisablePrevious bool   `json:"disable_previous"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			raw, err := base64.StdEncoding.DecodeString(body.Data)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			list := f.versions[key]
			rev := uint32(len(list) + 1)
			if body.DisablePrevious {
				for i := range list {
					list[i].enabled = false
				}
			}
			f.versions[key] = append(list, fakeVersion{revision: rev, data: raw, enabled: true})
			f.writes++
			_ = json.NewEncoder(w).Encode(map[string]any{"revision": rev})
		case len(parts) == 2 && parts[1] == "versions" && r.Method == http.MethodGet:
			f.mu.Lock()
			defer f.mu.Unlock()
			out := []map[string]any{}
			for _, v := range f.versions[key] {
				status := "disabled"
				if v.enabled {
					status = "enabled"
				}
				out = append(out, map[string]any{"revision": v.revision, "status": status})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"versions": out})
		case len(parts) == 3 && parts[1] == "versions":
			f.mu.Lock()
			defer f.mu.Unlock()
			list := f.versions[key]
			// The real API accepts "latest", "latest_enabled" and a number.
			switch parts[2] {
			case "latest_enabled":
				for i := len(list) - 1; i >= 0; i-- {
					if list[i].enabled {
						_ = json.NewEncoder(w).Encode(map[string]any{"revision": list[i].revision})
						return
					}
				}
			case "latest":
				if len(list) > 0 {
					_ = json.NewEncoder(w).Encode(map[string]any{"revision": list[len(list)-1].revision})
					return
				}
			default:
				rev, err := strconv.Atoi(parts[2])
				if err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				for _, v := range list {
					if v.revision == uint32(rev) {
						_ = json.NewEncoder(w).Encode(map[string]any{"revision": v.revision})
						return
					}
				}
			}
			w.WriteHeader(http.StatusNotFound)
		case len(parts) == 4 && parts[1] == "versions" && (parts[3] == "disable" || parts[3] == "enable"):
			rev, err := strconv.Atoi(parts[2])
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			list := f.versions[key]
			for i := range list {
				if list[i].revision == uint32(rev) {
					list[i].enabled = parts[3] == "enable"
					_ = json.NewEncoder(w).Encode(map[string]any{"revision": rev})
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	mux.HandleFunc("/secret-manager/v1beta1/regions/fr-par/secrets-by-path/versions/latest_enabled/access", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("secret_path") + "/" + r.URL.Query().Get("secret_name")
		f.mu.Lock()
		defer f.mu.Unlock()
		list := f.versions[key]
		for i := len(list) - 1; i >= 0; i-- {
			if list[i].enabled {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"revision": list[i].revision,
					"data":     base64.StdEncoding.EncodeToString(list[i].data),
				})
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"revision": 0, "data": ""})
	})

	return f, httptest.NewServer(mux)
}

// enabled returns the enabled versions for a key, which is what a reader sees.
func (f *scalewayFake) enabled(key string) []fakeVersion {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeVersion
	for _, v := range f.versions[key] {
		if v.enabled {
			out = append(out, v)
		}
	}
	return out
}

func TestScalewayStoreCompareAndSwap(t *testing.T) {
	const projectID = "22222222-2222-4222-8222-222222222222"
	const key = ACLPath + "/" + projectID

	fake, server := newScalewayFake(t)
	defer server.Close()
	store, err := NewScalewayStore(server.URL, "scw", "proj", "fr-par", server.Client())
	if err != nil {
		t.Fatalf("NewScalewayStore: %v", err)
	}
	ctx := t.Context()

	if err := store.CreateProject(ctx, projectID, "owner@beta.gouv.fr", "eidas2"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if got := len(fake.enabled(key)); got != 1 {
		t.Fatalf("after creation %d enabled versions, want 1", got)
	}

	// A normal update wins the window and leaves exactly one enabled version.
	if _, err := store.WriteACL(ctx, projectID, 1, []byte("owner:owner@beta.gouv.fr\nmember:a@beta.gouv.fr\n")); err != nil {
		t.Fatalf("WriteACL: %v", err)
	}
	if got := len(fake.enabled(key)); got != 1 {
		t.Fatalf("after update %d enabled versions, want 1", got)
	}
	data, revision, err := store.ReadACL(ctx, projectID)
	if err != nil {
		t.Fatalf("ReadACL: %v", err)
	}
	if revision != 2 || !strings.Contains(string(data), "member:a@beta.gouv.fr") {
		t.Fatalf("ReadACL = (%q, %d), want the new ACL at revision 2", data, revision)
	}

	// A stale base revision fails without writing anything.
	before := fake.writes
	if _, err := store.WriteACL(ctx, projectID, 1, []byte("owner:owner@beta.gouv.fr\n")); !errors.Is(err, ErrStoreConflict) {
		t.Fatalf("WriteACL(stale) = %v, want ErrStoreConflict", err)
	}
	if fake.writes != before {
		t.Fatalf("a stale write still reached the store (%d -> %d writes)", before, fake.writes)
	}
}

// TestScalewayStoreConcurrentWritersCannotBothWin reproduces the finding: a
// competing write lands between this writer's revision check and its append.
// Exactly one writer must win, the loser must report a conflict, and the
// winner's value must remain the enabled one.
func TestScalewayStoreConcurrentWritersCannotBothWin(t *testing.T) {
	const projectID = "33333333-3333-4333-8333-333333333333"
	const key = ACLPath + "/" + projectID

	fake, server := newScalewayFake(t)
	defer server.Close()
	store, err := NewScalewayStore(server.URL, "scw", "proj", "fr-par", server.Client())
	if err != nil {
		t.Fatalf("NewScalewayStore: %v", err)
	}
	ctx := t.Context()
	if err := store.CreateProject(ctx, projectID, "owner@beta.gouv.fr", "eidas2"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	const winnerACL = "owner:owner@beta.gouv.fr\nmember:winner@beta.gouv.fr\n"
	const loserACL = "owner:owner@beta.gouv.fr\nmember:loser@beta.gouv.fr\n"

	interleaved := false
	fake.beforeAppend = func() {
		if interleaved {
			return
		}
		interleaved = true
		// The competing writer commits version 2 before we append ours.
		id := fake.sids[key]
		other, err := NewScalewayStore(server.URL, "scw", "proj", "fr-par", server.Client())
		if err != nil {
			t.Errorf("NewScalewayStore: %v", err)
			return
		}
		fake.beforeAppend = nil
		if _, err := other.WriteACL(ctx, projectID, 1, []byte(winnerACL)); err != nil {
			t.Errorf("competing WriteACL: %v", err)
		}
		_ = id
	}

	_, err = store.WriteACL(ctx, projectID, 1, []byte(loserACL))
	if !errors.Is(err, ErrStoreConflict) {
		t.Fatalf("the losing concurrent write returned %v, want ErrStoreConflict", err)
	}

	data, _, err := store.ReadACL(ctx, projectID)
	if err != nil {
		t.Fatalf("ReadACL: %v", err)
	}
	if !strings.Contains(string(data), "member:winner@beta.gouv.fr") {
		t.Fatalf("the winner's ACL was overwritten; live ACL = %q", data)
	}
	if strings.Contains(string(data), "member:loser@beta.gouv.fr") {
		t.Fatalf("the loser's change became visible; live ACL = %q", data)
	}
	if got := len(fake.enabled(key)); got != 1 {
		t.Fatalf("%d enabled ACL versions after the race, want exactly 1", got)
	}
}

func TestScalewayStoreRejectsInvalidProjectIDBeforeAnyRequest(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	store, err := NewScalewayStore(server.URL, "scw", "proj", "fr-par", server.Client())
	if err != nil {
		t.Fatalf("NewScalewayStore: %v", err)
	}
	for _, bad := range []string{"", "../../etc", "not-a-uuid", "22222222-2222-1222-8222-222222222222"} {
		if _, _, err := store.ReadACL(t.Context(), bad); err == nil {
			t.Errorf("ReadACL(%q) succeeded", bad)
		}
	}
	if calls != 0 {
		t.Fatalf("%d upstream calls were made for invalid project IDs, want 0", calls)
	}
}

// TestScalewayStoreRemainsWritableAfterARetraction covers the finding that the
// earlier contiguity assumption wedged a project permanently: a retracted
// revision left a disabled gap that made every later write fail its base+1 test.
func TestScalewayStoreRemainsWritableAfterARetraction(t *testing.T) {
	const projectID = "44444444-4444-4444-8444-444444444444"
	const key = ACLPath + "/" + projectID

	fake, server := newScalewayFake(t)
	defer server.Close()
	store, err := NewScalewayStore(server.URL, "scw", "proj", "fr-par", server.Client())
	if err != nil {
		t.Fatalf("NewScalewayStore: %v", err)
	}
	ctx := t.Context()
	if err := store.CreateProject(ctx, projectID, "owner@beta.gouv.fr", "eidas2"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Force a lost race so a retracted (disabled) revision exists: version 3 is
	// appended and then disabled, leaving revisions 2 (enabled) and 3 (disabled).
	interleaved := false
	fake.beforeAppend = func() {
		if interleaved {
			return
		}
		interleaved = true
		other, err := NewScalewayStore(server.URL, "scw", "proj", "fr-par", server.Client())
		if err != nil {
			t.Errorf("NewScalewayStore: %v", err)
			return
		}
		fake.beforeAppend = nil
		if _, err := other.WriteACL(ctx, projectID, 1, []byte("owner:owner@beta.gouv.fr\nmember:winner@beta.gouv.fr\n")); err != nil {
			t.Errorf("winner WriteACL: %v", err)
		}
	}
	if _, err := store.WriteACL(ctx, projectID, 1, []byte("owner:owner@beta.gouv.fr\nmember:loser@beta.gouv.fr\n")); !errors.Is(err, ErrStoreConflict) {
		t.Fatalf("losing write = %v, want ErrStoreConflict", err)
	}
	fake.mu.Lock()
	total := len(fake.versions[key])
	fake.mu.Unlock()
	if total < 3 {
		t.Fatalf("expected a retracted revision to exist (%d versions recorded)", total)
	}

	// The project must still be writable: the next writer appends beyond the gap.
	data, revision, err := store.ReadACL(ctx, projectID)
	if err != nil {
		t.Fatalf("ReadACL after retraction: %v", err)
	}
	newRec, err := store.WriteACL(ctx, projectID, revision, []byte("owner:owner@beta.gouv.fr\nmember:after@beta.gouv.fr\n"))
	if err != nil {
		t.Fatalf("WriteACL after a retraction failed; the project is wedged: %v", err)
	}
	if newRec <= revision {
		t.Fatalf("revision went from %d to %d; it must increase", revision, newRec)
	}
	live, _, err := store.ReadACL(ctx, projectID)
	if err != nil {
		t.Fatalf("ReadACL: %v", err)
	}
	if !strings.Contains(string(live), "member:after@beta.gouv.fr") {
		t.Fatalf("the post-retraction write is not live; ACL = %q (previous %q)", live, data)
	}
	if got := len(fake.enabled(key)); got != 1 {
		t.Fatalf("%d enabled ACL versions, want exactly 1", got)
	}
}
