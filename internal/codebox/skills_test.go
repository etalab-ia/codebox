package codebox

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSkillFrontmatter(t *testing.T) {
	data := []byte("---\nname: rgaa\ndescription: >-\n  Audit the RGAA.\n  Preserve folded lines.\n---\n# Skill\n")
	meta, err := parseSkillFrontmatter(data)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Name != "rgaa" || !strings.Contains(meta.Description, "Preserve folded lines") {
		t.Fatalf("frontmatter = %+v", meta)
	}
	for _, invalid := range [][]byte{
		[]byte("# No frontmatter\n"),
		[]byte("---\nname: rgaa\n---\n"),
		[]byte("---\nname: [\n---\n"),
	} {
		if _, err := parseSkillFrontmatter(invalid); err == nil {
			t.Fatalf("invalid frontmatter unexpectedly parsed: %q", invalid)
		}
	}
}

func TestProjectSkillsRequireMicrosandboxRegardlessOfHostSupport(t *testing.T) {
	if err := validateProjectSkillRuntime(RuntimeMicrosandbox, []string{"official/rgaa"}); err != nil {
		t.Fatalf("Microsandbox skill selection: %v", err)
	}
	for _, runtime := range []Runtime{RuntimeTart, RuntimeAgentVM} {
		if err := validateProjectSkillRuntime(runtime, []string{"official/rgaa"}); err == nil || !strings.Contains(err.Error(), "sealed microsandbox") {
			t.Errorf("runtime %q validation error = %v", runtime, err)
		}
	}
	if err := validateProjectSkillRuntime(RuntimeAgentVM, nil); err != nil {
		t.Fatalf("a project with no skills must not be restricted: %v", err)
	}
}

func TestParseProjectSkillCatalogueRejectsDuplicateNames(t *testing.T) {
	entries := []skillTreeEntry{
		{Mode: "100644", Path: "skills/rgaa/SKILL.md", OID: "one"},
		{Mode: "100644", Path: "skills/.experimental/rgaa/SKILL.md", OID: "two"},
	}
	blobs := map[string][]byte{
		"one": []byte("---\nname: rgaa\ndescription: official\n---\n"),
		"two": []byte("---\nname: rgaa\ndescription: experimental\n---\n"),
	}
	_, err := parseProjectSkillCatalogue(entries, func(oid string) ([]byte, error) { return blobs[oid], nil })
	if err == nil || !strings.Contains(err.Error(), "duplicate skill name") {
		t.Fatalf("duplicate name error = %v", err)
	}
}

func TestParseProjectSkillCatalogueRejectsSymlinkSkill(t *testing.T) {
	entries := []skillTreeEntry{{Mode: "120000", Path: "skills/rgaa/SKILL.md", OID: "one"}}
	_, err := parseProjectSkillCatalogue(entries, func(string) ([]byte, error) { return nil, nil })
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("symlink error = %v", err)
	}
}

func TestNormalizeSkillArchiveVerifiesAndIsDeterministic(t *testing.T) {
	raw := skillSourceTar(t,
		skillTarEntry{name: "skills/rgaa/SKILL.md", body: []byte("---\nname: rgaa\ndescription: skill instructions\n---\n# RGAA\n")},
		skillTarEntry{name: "skills/rgaa/references/guide.md", body: []byte("reference\n")},
	)
	archive, err := normalizeSkillArchive(bytes.NewReader(raw), "skills/rgaa", "rgaa")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateNormalizedSkillArchive(archive, "rgaa"); err != nil {
		t.Fatalf("normalized archive: %v", err)
	}
	second, err := normalizeSkillArchive(bytes.NewReader(raw), "skills/rgaa", "rgaa")
	if err != nil || !bytes.Equal(archive, second) {
		t.Fatalf("archive is not reproducible: %v", err)
	}
	tampered := append([]byte(nil), archive...)
	tampered[len(tampered)/2] ^= 0xff
	if err := validateNormalizedSkillArchive(tampered, "rgaa"); err == nil {
		t.Fatal("corrupt normalized archive was accepted")
	}
}

func TestNormalizeSkillArchiveRejectsUnsafeEntries(t *testing.T) {
	valid := skillTarEntry{name: "skills/rgaa/SKILL.md", body: []byte("---\nname: rgaa\ndescription: skill\n---\n")}
	cases := []struct {
		name    string
		entries []skillTarEntry
		want    string
	}{
		{"parent traversal", []skillTarEntry{valid, {name: "skills/rgaa/../outside.md", body: []byte("x")}}, "unsafe archive entry"},
		{"symlink", []skillTarEntry{valid, {name: "skills/rgaa/ref", kind: tar.TypeSymlink, link: "../../secret"}}, "not a regular file"},
		{"duplicate", []skillTarEntry{valid, valid}, "duplicate path"},
		{"unexpected directory", []skillTarEntry{{name: "skills/other/SKILL.md", body: []byte("---\nname: rgaa\ndescription: skill\n---\n")}}, "outside selected skill"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := skillSourceTar(t, tc.entries...)
			_, err := normalizeSkillArchive(bytes.NewReader(raw), "skills/rgaa", "rgaa")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestManagedInstructionsPreserveUnmanagedBytes(t *testing.T) {
	lock := SkillLock{Repository: projectSkillsRepository, Revision: strings.Repeat("a", 40), SHA256: strings.Repeat("b", 64)}
	original := "User-authored prefix\r\n\r\nMore rules.\n"
	got, err := MergeManagedInstructions(original, []string{"official/rgaa"}, map[string]SkillLock{"official/rgaa": lock}, RuntimeMicrosandbox)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, original) || !strings.Contains(got, "official/rgaa") || !strings.Contains(got, lock.Revision) {
		t.Fatalf("managed result did not preserve the prefix or render the pin: %q", got)
	}
	updatedLock := lock
	updatedLock.Revision = strings.Repeat("c", 40)
	updated, err := MergeManagedInstructions(got, []string{"official/rgaa"}, map[string]SkillLock{"official/rgaa": updatedLock}, RuntimeMicrosandbox)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(updated, original) || strings.Contains(updated, lock.Revision) || !strings.Contains(updated, updatedLock.Revision) {
		t.Fatalf("updating the managed zone changed user text or left an old pin: %q", updated)
	}
	zone, err := managedSkillZone(updated)
	if err != nil || !strings.Contains(zone, updatedLock.Revision) {
		t.Fatalf("zone = %q, err = %v", zone, err)
	}
	cleared, err := MergeManagedInstructions(updated, nil, nil, RuntimeMicrosandbox)
	if err != nil || !strings.Contains(cleared, original) || strings.Contains(cleared, "official/rgaa") ||
		!strings.Contains(cleared, "BEGIN CODEBOX MANAGED SKILLS") ||
		!strings.Contains(cleared, "## Managed codebox instructions") {
		t.Fatalf("cleared = %q, err = %v", cleared, err)
	}
}

func TestManagedInstructionsRejectAmbiguousMarkers(t *testing.T) {
	for _, text := range []string{
		"<!-- BEGIN CODEBOX MANAGED SKILLS -->\nno end\n",
		"<!-- END CODEBOX MANAGED SKILLS -->\nno start\n",
		"<!-- BEGIN CODEBOX MANAGED SKILLS -->\n<!-- BEGIN CODEBOX MANAGED SKILLS -->\n<!-- END CODEBOX MANAGED SKILLS -->\n",
		"<!-- END CODEBOX MANAGED SKILLS -->\n<!-- BEGIN CODEBOX MANAGED SKILLS -->\n",
	} {
		if _, err := MergeManagedInstructions(text, []string{"official/rgaa"}, map[string]SkillLock{"official/rgaa": {Revision: strings.Repeat("a", 40)}}, RuntimeMicrosandbox); err == nil {
			t.Fatalf("ambiguous managed section was accepted: %q", text)
		}
	}
}

func TestManagedInstructionsScopeStandingGuidanceToMicrosandbox(t *testing.T) {
	lock := SkillLock{Repository: projectSkillsRepository, Revision: strings.Repeat("a", 40), SHA256: strings.Repeat("b", 64)}
	// Tart has no host-loopback port forward: pins render, the web-preview
	// guidance must not.
	pins, err := MergeManagedInstructions("User rules.\n", []string{"official/rgaa"}, map[string]SkillLock{"official/rgaa": lock}, RuntimeTart)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pins, "official/rgaa") || strings.Contains(pins, "Web preview") || strings.Contains(pins, "## Managed codebox instructions") {
		t.Fatalf("tart zone must carry pins without Microsandbox guidance: %q", pins)
	}
	// Without pins there is nothing runtime-neutral to say: no zone at all.
	none, err := MergeManagedInstructions(pins, nil, nil, RuntimeTart)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(none, "BEGIN CODEBOX MANAGED SKILLS") {
		t.Fatalf("pin-less tart selection must remove the managed zone: %q", none)
	}
}

func TestManagedInstructionRuntimeIgnoresHostOS(t *testing.T) {
	// A manifest stores the runtime chosen where the project was created;
	// regenerating instructions elsewhere (skill update, rollback, recovery)
	// must not reinterpret that choice through the current host's runtime
	// support. On a Windows host the old parseRuntimeName-based lookup
	// rewrote tart and agent-vm manifests into Microsandbox guidance.
	if got := managedInstructionRuntime(string(RuntimeTart)); got != RuntimeTart {
		t.Fatalf("stored tart manifest must render as tart on every host, got %s", got)
	}
	if got := managedInstructionRuntime(string(RuntimeAgentVM)); got != RuntimeAgentVM {
		t.Fatalf("stored agent-vm manifest must render as agent-vm on every host, got %s", got)
	}
	if got := managedInstructionRuntime(string(RuntimeMicrosandbox)); got != RuntimeMicrosandbox {
		t.Fatalf("stored microsandbox manifest must render as microsandbox, got %s", got)
	}
	if got := managedInstructionRuntime("not-a-runtime"); got != RuntimeMicrosandbox {
		t.Fatalf("an unparsable stored runtime must fall back to microsandbox, got %s", got)
	}
}

func TestLocalSkillSelectionsAreHostScopedAndValidated(t *testing.T) {
	root := t.TempDir()
	path, err := LocalSkillSelectionsPath(root, "cb-project-1234")
	if err != nil {
		t.Fatal(err)
	}
	selection := LocalSkillSelections{Skills: []string{"official/rgaa"}, Pins: map[string]SkillLock{"official/rgaa": {Revision: strings.Repeat("a", 40), SHA256: strings.Repeat("b", 64)}}}
	if err := WriteLocalSkillSelections(DefaultFS, path, selection); err != nil {
		t.Fatal(err)
	}
	got, err := ReadLocalSkillSelections(DefaultFS, path)
	if err != nil || len(got.Skills) != 1 || got.Skills[0] != "official/rgaa" {
		t.Fatalf("selection = %+v, err = %v", got, err)
	}
	if strings.Contains(path, root+string(filepath.Separator)+"checkout") {
		t.Fatalf("local skill selections unexpectedly live in a project checkout: %s", path)
	}
	if _, err := LocalSkillSelectionsPath(root, "../../outside"); err == nil {
		t.Fatal("path traversal instance name was accepted")
	}
}

func TestLoadProjectSkillStateReportsLocalOnlyMode(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	manifestPath := ProjectManifestPath(root)
	if err := WriteProjectManifest(DefaultFS, manifestPath, ProjectManifest{SkillsLocalOnly: true}); err != nil {
		t.Fatal(err)
	}
	_, ids, managed, localOnly, err := LoadProjectSkillState(DefaultFS, root, state, "cb-project-1234")
	if err != nil {
		t.Fatal(err)
	}
	if !managed || !localOnly || len(ids) != 0 {
		t.Fatalf("local-only state = managed:%v localOnly:%v ids:%v", managed, localOnly, ids)
	}
}

func TestValidateProjectSkillIDsRejectsNestedCategoryPrefixes(t *testing.T) {
	for _, id := range []string{"official/experimental/foo", "experimental/official/foo", "official/foo/bar"} {
		if err := ValidateProjectSkillIDs([]string{id}); err == nil {
			t.Errorf("malformed skill ID %q was accepted", id)
		}
	}
}

func TestLoadLockedSkillPackagesUsesOnlyVerifiedCache(t *testing.T) {
	oldCacheDir := userCacheDirFn
	cache := t.TempDir()
	userCacheDirFn = func() (string, error) { return cache, nil }
	t.Cleanup(func() { userCacheDirFn = oldCacheDir })
	raw := skillSourceTar(t, skillTarEntry{name: "skills/rgaa/SKILL.md", body: []byte("---\nname: rgaa\ndescription: skill\n---\n")})
	archive, err := normalizeSkillArchive(bytes.NewReader(raw), "skills/rgaa", "rgaa")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(archive)
	lock := SkillLock{Repository: projectSkillsRepository, Revision: strings.Repeat("a", 40), SHA256: hex.EncodeToString(digest[:])}
	skill := ProjectSkill{ID: "official/rgaa", Name: "rgaa"}
	if err := cacheSkillArchive(skill, lock, archive); err != nil {
		t.Fatal(err)
	}
	packages, err := LoadLockedSkillPackages([]string{skill.ID}, map[string]SkillLock{skill.ID: lock})
	if err != nil || len(packages) != 1 || !bytes.Equal(packages[0].Archive, archive) {
		t.Fatalf("packages = %d, err = %v", len(packages), err)
	}
	malformedID := "official/experimental/rgaa"
	if _, err := LoadLockedSkillPackages([]string{malformedID}, map[string]SkillLock{malformedID: lock}); err == nil || !strings.Contains(err.Error(), "invalid skill id") {
		t.Fatalf("nested category prefix was not rejected before cache access: %v", err)
	}
	cachePath, err := skillArchiveCachePath(skill.Name, lock)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(cachePath); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLockedSkillPackages([]string{skill.ID}, map[string]SkillLock{skill.ID: lock}); err == nil || !strings.Contains(err.Error(), "offline cache") {
		t.Fatalf("missing offline archive error = %v", err)
	}
	if err := os.WriteFile(cachePath, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLockedSkillPackages([]string{skill.ID}, map[string]SkillLock{skill.ID: lock}); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("corrupt cache error = %v", err)
	}
}

type skillTarEntry struct {
	name string
	body []byte
	kind byte
	link string
}

func skillSourceTar(t *testing.T, entries ...skillTarEntry) []byte {
	t.Helper()
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	allEntries := append([]skillTarEntry{{name: "LICENSE", body: []byte("MIT License\n")}}, entries...)
	for _, entry := range allEntries {
		kind := entry.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		h := &tar.Header{Name: entry.name, Mode: 0o644, Size: int64(len(entry.body)), Typeflag: kind, Linkname: entry.link}
		if kind == tar.TypeSymlink {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if kind == tar.TypeReg {
			if _, err := tw.Write(entry.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestSearchProjectSkillsMatchesWordsAcrossFields(t *testing.T) {
	catalogue := []ProjectSkill{
		{ID: "official/react", Name: "React UI", Description: "Build component interfaces"},
		{ID: "official/python", Name: "Python", Description: "Data analysis"},
		{ID: "experimental/react-native", Name: "React Native", Description: "Mobile interfaces"},
	}
	got := SearchProjectSkills(catalogue, "REACT interfaces")
	if len(got) != 2 || got[0].ID != "official/react" || got[1].ID != "experimental/react-native" {
		t.Fatalf("search results = %+v", got)
	}
	if got := SearchProjectSkills(catalogue, "   "); len(got) != len(catalogue) {
		t.Fatalf("empty search should list the catalogue, got %+v", got)
	}
}

func fixtureSkillResolver(t *testing.T) func(context.Context, []string) ([]ProjectSkill, map[string]SkillLock, error) {
	t.Helper()
	return func(_ context.Context, ids []string) ([]ProjectSkill, map[string]SkillLock, error) {
		selected := make([]ProjectSkill, 0, len(ids))
		locks := make(map[string]SkillLock, len(ids))
		for _, id := range ids {
			category, name, ok := strings.Cut(id, "/")
			if !ok {
				return nil, nil, fmt.Errorf("invalid test skill id %q", id)
			}
			sourcePath := "skills/" + name
			if category == "experimental" {
				sourcePath = "skills/.experimental/" + name
			}
			raw := skillSourceTar(t, skillTarEntry{
				name: sourcePath + "/SKILL.md",
				body: []byte("---\nname: " + name + "\ndescription: fixture\n---\n# Fixture\n"),
			})
			archive, err := normalizeSkillArchive(bytes.NewReader(raw), sourcePath, name)
			if err != nil {
				return nil, nil, err
			}
			digest := sha256.Sum256(archive)
			lock := SkillLock{Repository: projectSkillsRepository, Revision: projectSkillsRevision, SHA256: hex.EncodeToString(digest[:])}
			skill := ProjectSkill{ID: id, Name: name, Path: sourcePath}
			if err := cacheSkillArchive(skill, lock, archive); err != nil {
				return nil, nil, err
			}
			selected = append(selected, skill)
			locks[id] = lock
		}
		return selected, locks, nil
	}
}
