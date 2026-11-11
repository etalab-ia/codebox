package codebox

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// readArchiveEntries decodes a transfer payload into name -> header. The
// payload is a gzip stream, so assertions on its raw bytes prove nothing:
// every property under test has to be read out of the decoded entries.
func readArchiveEntries(t *testing.T, payload []byte) map[string]*tar.Header {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	out := map[string]*tar.Header{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		out[h.Name] = h
	}
	return out
}

// TestTransferArchivePreservesModificationTimes pins #194: every entry was
// stamped with the Unix epoch, so a synced file looked older than any source
// and date-comparing tools (make, bundlers, build caches) rebuilt everything
// on each sync.
func TestTransferArchivePreservesModificationTimes(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "a\n")
	writeFile(t, dir, "b/c.txt", "c\n")
	gitInitForTransfer(t, dir)

	// A distinctive, non-epoch time carrying sub-second precision. The
	// nanoseconds are the point: a USTAR header stores whole seconds, so an
	// exact-comparison assertion with a zero-nanosecond stamp would pass even
	// while the value was being rounded. Two files inside one second is the
	// case where rounding collapses their order, which is what the build
	// tools this protects actually act on.
	stamp := time.Date(2021, 3, 4, 5, 6, 7, 800000000, time.UTC)
	for _, rel := range []string{"a.txt", filepath.Join("b", "c.txt")} {
		if err := os.Chtimes(filepath.Join(dir, rel), stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}

	man, err := ResolveTransferSet(context.Background(), dir, TransferOptions{})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := BuildTransferArchive(man)
	if err != nil {
		t.Fatal(err)
	}
	entries := readArchiveEntries(t, payload)

	for _, rel := range []string{"a.txt", "b/c.txt"} {
		h, ok := entries[rel]
		if !ok {
			t.Fatalf("%s missing from the archive: %v", rel, len(entries))
		}
		if h.ModTime.UTC().IsZero() || h.ModTime.UTC().Unix() == 0 {
			t.Fatalf("%s carries the epoch timestamp: %v", rel, h.ModTime)
		}
		if !h.ModTime.UTC().Equal(stamp) {
			t.Fatalf("%s mtime = %v, want %v", rel, h.ModTime.UTC(), stamp)
		}
	}
}

// TestTransferArchiveKeepsSubSecondOrderWithinASecond pins the second half of
// the same defect: two files created inside one second must not come out with
// the same timestamp, or a tool comparing them has no ordering to work with.
func TestTransferArchiveKeepsSubSecondOrderWithinASecond(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "early.txt", "e\n")
	writeFile(t, dir, "late.txt", "l\n")
	gitInitForTransfer(t, dir)

	early := time.Date(2021, 3, 4, 5, 6, 7, 100000000, time.UTC)
	late := time.Date(2021, 3, 4, 5, 6, 7, 400000000, time.UTC)
	// Both fall in the same rounding bucket: Go rounds a sub-second mtime to
	// the NEAREST second, so 100ms and 400ms both become 05:06:07 in a
	// whole-second header and collapse to one value. That is what this test
	// pins — the assertion below is exactly what a rounded header cannot
	// satisfy. (800ms apart would not do it: 900ms rounds UP to :08, so the
	// two values stay distinct and ordered even without PAX.)
	for name, ts := range map[string]time.Time{"early.txt": early, "late.txt": late} {
		if err := os.Chtimes(filepath.Join(dir, name), ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	man, err := ResolveTransferSet(context.Background(), dir, TransferOptions{})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := BuildTransferArchive(man)
	if err != nil {
		t.Fatal(err)
	}
	entries := readArchiveEntries(t, payload)
	e, l := entries["early.txt"], entries["late.txt"]
	if e == nil || l == nil {
		t.Fatalf("both files must be in the archive: %v", len(entries))
	}
	if e.ModTime.UTC().Equal(l.ModTime.UTC()) {
		t.Fatalf("files created 300ms apart collapsed to the same mtime %v; "+
			"a date-comparing tool has no order to act on", e.ModTime.UTC())
	}
	if !e.ModTime.UTC().Before(l.ModTime.UTC()) {
		t.Fatalf("order reversed: early=%v late=%v", e.ModTime.UTC(), l.ModTime.UTC())
	}
}

// TestTransferArchiveIsReproducibleForTheSameInput guards the property the
// fixed epoch was originally there to provide. Preserving real mtimes keeps it,
// because the same files carry the same mtimes — but only if nothing else in
// the payload varies, so it is worth asserting rather than assuming.
func TestTransferArchiveIsReproducibleForTheSameInput(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "a\n")
	writeFile(t, dir, "b/c.txt", "c\n")
	gitInitForTransfer(t, dir)
	stamp := time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)
	for _, rel := range []string{"a.txt", filepath.Join("b", "c.txt")} {
		if err := os.Chtimes(filepath.Join(dir, rel), stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	man, err := ResolveTransferSet(context.Background(), dir, TransferOptions{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := BuildTransferArchive(man)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildTransferArchive(man)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("two builds of the same unchanged manifest differ")
	}
}
