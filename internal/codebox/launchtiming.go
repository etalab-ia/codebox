package codebox

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// launchTimingsFile records how long launches actually took on this machine,
// so the next announcement is a measurement instead of an invented number.
// Like the OpenCode trust record it is host-local and never travels with the
// project: a duration belongs to a machine and to its cache state.
const launchTimingsFile = "launch-timings.json"

// maxLaunchSamples bounds the history kept per cache state. The spread of the
// last few launches is what the hint needs; an unbounded list only grows the
// file.
const maxLaunchSamples = 10

// LaunchKind distinguishes the two cache states a duration is meaningless
// without: a launch that had to install the toolchain is not comparable to one
// that reused it.
type LaunchKind string

const (
	// LaunchCold: the image had to be pulled or the toolchain installed.
	LaunchCold LaunchKind = "cold"
	// LaunchWarm: everything was already there.
	LaunchWarm LaunchKind = "warm"
)

type launchTimingEntry struct {
	// ColdSeconds and WarmSeconds hold the observed samples for each cache
	// state, oldest first.
	ColdSeconds      []int64 `json:"cold_seconds,omitempty"`
	WarmSeconds      []int64 `json:"warm_seconds,omitempty"`
	FirstColdSeconds int64   `json:"first_cold_seconds,omitempty"`
	// FirstMeasuredAt is when the very first observation was recorded.
	FirstMeasuredAt string `json:"first_measured_at,omitempty"`
}

type launchTimingRecord struct {
	Version int `json:"version"`
	// Entries is keyed by the cache-state fingerprint.
	Entries map[string]launchTimingEntry `json:"entries"`
}

// LaunchTimingStore persists and reads launch duration observations. A store
// that cannot read its file degrades to "no measurement", never to an error
// that stops a launch.
type LaunchTimingStore struct {
	fs  FS
	dir string
}

func NewLaunchTimingStore(fs FS, dir string) *LaunchTimingStore {
	return &LaunchTimingStore{fs: fs, dir: dir}
}

// DefaultLaunchTimingStore points at the codebox state directory.
func DefaultLaunchTimingStore() *LaunchTimingStore {
	return NewLaunchTimingStore(DefaultFS, DefaultStateDir())
}

// LaunchTimingKey fingerprints the cache state a launch duration belongs to:
// guest image, pinned toolchain version, isolation level, guest sizing and
// platform. Any change to those means the machine has to install again, so a
// warm launch must never inherit the cold launch's history or vice versa.
func LaunchTimingKey(image, toolchainVersion string, isolation Isolation, cpus, memoryMB int, goos, arch string) string {
	fields := []string{
		image,
		toolchainVersion,
		string(isolation),
		fmt.Sprintf("%d", cpus),
		fmt.Sprintf("%d", memoryMB),
		goos,
		arch,
	}
	h := sha256.New()
	h.Write([]byte(strings.Join(fields, "\x00")))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// LaunchTimingKeyFor builds the key for a runtime configuration. Missing
// sizing falls back to the runtime defaults so the key describes the guest
// that will actually be created.
func LaunchTimingKeyFor(cfg Config, image string) string {
	goos := runtime.GOOS
	arch := runtime.GOARCH
	return LaunchTimingKey(image, msbBrowserOpenCodeVersion, cfg.Isolation, cfg.CPUs, cfg.MemoryMB, goos, arch)
}

func (s *LaunchTimingStore) path() string {
	return filepath.Join(s.dir, launchTimingsFile)
}

// load returns the record, or an empty one when the file is absent or
// unparseable. A corrupt timing file is never a reason to refuse a launch.
func (s *LaunchTimingStore) load() launchTimingRecord {
	empty := launchTimingRecord{Version: 1, Entries: map[string]launchTimingEntry{}}
	if s == nil {
		return empty
	}
	data, err := s.fs.ReadFile(s.path())
	if err != nil {
		return empty
	}
	var rec launchTimingRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return empty
	}
	if rec.Entries == nil {
		rec.Entries = map[string]launchTimingEntry{}
	}
	rec.Version = 1
	return rec
}

// Observe records one successful launch duration for a cache state.
func (s *LaunchTimingStore) Observe(key string, kind LaunchKind, took time.Duration) error {
	if s == nil || key == "" || took <= 0 {
		return nil
	}
	seconds := int64(took.Round(time.Second) / time.Second)
	if seconds <= 0 {
		seconds = 1
	}
	rec := s.load()
	entry := rec.Entries[key]
	if kind == LaunchCold && entry.FirstColdSeconds == 0 {
		entry.FirstColdSeconds = seconds
	}
	target := &entry.ColdSeconds
	if kind == LaunchWarm {
		target = &entry.WarmSeconds
	}
	*target = append(*target, seconds)
	if len(*target) > maxLaunchSamples {
		*target = (*target)[len(*target)-maxLaunchSamples:]
	}
	if entry.FirstMeasuredAt == "" {
		entry.FirstMeasuredAt = time.Now().UTC().Format(time.RFC3339)
	}
	rec.Entries[key] = entry
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := s.fs.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create the launch timing directory: %w", err)
	}
	return atomicWrite(s.fs, s.path(), append(data, '\n'), 0o600)
}

// Observation returns the previously measured range for a cache state. ok is
// false when nothing has been measured, which is the state of a machine that
// has never run this configuration.
func (s *LaunchTimingStore) Observation(key string, kind LaunchKind) (low, high int64, ok bool) {
	rec := s.load()
	entry := rec.Entries[key]
	samples := entry.ColdSeconds
	if kind == LaunchWarm {
		samples = entry.WarmSeconds
	}
	// A hand-edited or future file can carry zeros or negatives; they are not
	// durations and must not produce a promise of "0 s".
	var valid []int64
	for _, v := range samples {
		if v > 0 {
			valid = append(valid, v)
		}
	}
	if len(valid) == 0 {
		return 0, 0, false
	}
	sort.Slice(valid, func(i, j int) bool { return valid[i] < valid[j] })
	return valid[0], valid[len(valid)-1], true
}

// HasAnyObservation reports whether this cache state has been measured at all.
func (s *LaunchTimingStore) HasAnyObservation(key string) bool {
	rec := s.load()
	entry := rec.Entries[key]
	return len(entry.ColdSeconds) > 0 || len(entry.WarmSeconds) > 0
}

// FormatLaunchSeconds renders a measured duration for the announcement. It is
// coarser than a stopwatch on purpose: the promise is an order of magnitude,
// not a deadline.
func FormatLaunchSeconds(seconds int64) string {
	switch {
	case seconds <= 0:
		return ""
	case seconds == 1:
		return "1 seconde"
	case seconds < 60:
		return fmt.Sprintf("%d secondes", seconds)
	case seconds < 90:
		return "environ 1 minute"
	default:
		return fmt.Sprintf("environ %d minutes", seconds/60)
	}
}

// LaunchDurationPhrase is the measured part of the announcement for a cache
// state, or the honest fallback when this machine has not been measured yet.
func LaunchDurationPhrase(store *LaunchTimingStore, key string, kind LaunchKind) string {
	low, high, ok := store.Observation(key, kind)
	if !ok {
		if kind == LaunchCold {
			return "cela peut prendre plusieurs minutes"
		}
		return "durée pas encore mesurée sur cette machine"
	}
	if low == high || high == 0 {
		return FormatLaunchSeconds(low)
	}
	return fmt.Sprintf("%s à %s", FormatLaunchSeconds(low), FormatLaunchSeconds(high))
}
