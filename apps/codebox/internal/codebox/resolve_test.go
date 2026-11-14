package codebox

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// mapFS is an in-memory FS for the reader/writer tests. It mirrors realFS's
// contract exactly: WriteTemp creates a temp entry, RenameTmp moves it, and
// failRename simulates a crash between write and rename.
type mapFS struct {
	// mu guards the maps: tests exercise cross-process locking through
	// mapFS, so concurrent goroutines hit it from multiple registry objects.
	mu         sync.Mutex
	files      map[string][]byte
	perms      map[string]os.FileMode
	failRename bool
}

func newMapFS() *mapFS {
	return &mapFS{files: map[string][]byte{}, perms: map[string]os.FileMode{}}
}

func (m *mapFS) ReadFile(path string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if data, ok := m.files[path]; ok {
		return data, nil
	}
	return nil, os.ErrNotExist
}

func (m *mapFS) WriteFile(path string, data []byte, perm os.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[path] = data
	m.perms[path] = perm
	return nil
}

func (m *mapFS) MkdirAll(path string, perm os.FileMode) error { return nil }

func (m *mapFS) Remove(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.files, path)
	return nil
}

func (m *mapFS) RenameTmp(oldPath, newPath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failRename {
		return os.ErrPermission
	}
	data, ok := m.files[oldPath]
	if !ok {
		return os.ErrNotExist
	}
	m.files[newPath] = data
	m.perms[newPath] = m.perms[oldPath]
	delete(m.files, oldPath)
	delete(m.perms, oldPath)
	return nil
}

func (m *mapFS) WriteTemp(dir, base string, data []byte, perm os.FileMode) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	name := dir + string(filepath.Separator) + "." + base + ".tmp-1"
	m.files[name] = data
	m.perms[name] = perm
	return name, nil
}

func (m *mapFS) CreateExclusive(path string, data []byte, perm os.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.files[path]; exists {
		return os.ErrExist
	}
	m.files[path] = data
	m.perms[path] = perm
	return nil
}

func TestResolvePrecedence(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		def     string
		flag    map[string]string
		env     map[string]string
		project map[string]string
		user    map[string]string
		want    string
		src     Source
	}{
		{
			name:    "flag wins over everything",
			key:     "runtime",
			def:     "microsandbox",
			flag:    map[string]string{"runtime": "tart"},
			env:     map[string]string{"runtime": "agent-vm"},
			project: map[string]string{"runtime": "microsandbox"},
			user:    map[string]string{"runtime": "microsandbox"},
			want:    "tart",
			src:     SourceFlag,
		},
		{
			name:    "env wins over project and user",
			key:     "isolation",
			def:     "backend",
			env:     map[string]string{"isolation": "full"},
			project: map[string]string{"isolation": "backend"},
			user:    map[string]string{"isolation": "backend"},
			want:    "full",
			src:     SourceEnv,
		},
		{
			name:    "project wins over user",
			key:     "model",
			def:     "",
			project: map[string]string{"model": "albert/x"},
			user:    map[string]string{"model": "albert/y"},
			want:    "albert/x",
			src:     SourceProject,
		},
		{
			name: "user wins over default",
			key:  "model",
			def:  "albert/default",
			user: map[string]string{"model": "albert/y"},
			want: "albert/y",
			src:  SourceUser,
		},
		{
			name: "default when nothing set",
			key:  "runtime",
			def:  "microsandbox",
			want: "microsandbox",
			src:  SourceDefault,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := Settings{Flag: tt.flag, Env: tt.env, Project: tt.project, User: tt.user}
			got := s.resolveValue(tt.key, tt.def)
			if got.Value != tt.want {
				t.Errorf("Value = %q, want %q", got.Value, tt.want)
			}
			if got.Source != tt.src {
				t.Errorf("Source = %v, want %v", got.Source, tt.src)
			}
		})
	}
}

func TestResolvePrecedenceValues(t *testing.T) {
	// Value-level checks, separate from the source-level table above.
	s := Settings{
		Flag:    map[string]string{"runtime": "tart"},
		Env:     map[string]string{"isolation": "full"},
		Project: map[string]string{"model": "albert/x"},
		User:    map[string]string{"credential_ref": "main"},
	}
	if got := s.resolveValue("runtime", "microsandbox"); got.Value != "tart" || !got.Set {
		t.Errorf("runtime = %+v, want tart/set", got)
	}
	if got := s.resolveValue("isolation", "backend"); got.Value != "full" {
		t.Errorf("isolation = %+v, want full", got)
	}
	if got := s.resolveValue("model", ""); got.Value != "albert/x" || got.Source != SourceProject {
		t.Errorf("model = %+v, want albert/x from project", got)
	}
	if got := s.resolveValue("credential_ref", ""); got.Value != "main" || got.Source != SourceUser {
		t.Errorf("credential_ref = %+v, want main from user", got)
	}
	if got := s.resolveValue("workspace_dir", "./workspace"); got.Value != "./workspace" || got.Set {
		t.Errorf("workspace_dir = %+v, want default unset", got)
	}
}

func TestResolveExplicitEmptyWins(t *testing.T) {
	// An explicitly empty value means "turned off": it beats every lower
	// source, the same contract the legacy Config applies to the server
	// password.
	s := Settings{
		Env:     map[string]string{"model": ""},
		Project: map[string]string{"model": "albert/x"},
		User:    map[string]string{"model": "albert/y"},
	}
	got := s.resolveValue("model", "albert/default")
	if !got.Set || got.Value != "" {
		t.Errorf("model = %+v, want explicit empty (Set=true, Value=\"\")", got)
	}
	if got.Source != SourceEnv {
		t.Errorf("Source = %v, want env", got.Source)
	}
}

func TestResolveUnsetVsExplicitEmptyDistinct(t *testing.T) {
	s := Settings{}
	got := s.resolveValue("model", "albert/default")
	if got.Set {
		t.Error("Set = true for unset field, want false")
	}
	if got.Source != SourceDefault {
		t.Errorf("Source = %v, want default", got.Source)
	}
}

func TestEnvSettingsOnlyNamespaced(t *testing.T) {
	lookup := lookupFrom(map[string]string{
		"CODEBOX_RUNTIME":   "tart",
		"CODEBOX_ISOLATION": "full",
		"RUNTIME":           "agent-vm", // legacy: must NOT be read
		"ISOLATION":         "backend",  // legacy: must NOT be read
		"CODEBOX_UNKNOWN":   "x",        // unknown namespaced: ignored
	})
	env := EnvSettings(lookup)
	if env["runtime"] != "tart" {
		t.Errorf("runtime = %q, want tart", env["runtime"])
	}
	if env["isolation"] != "full" {
		t.Errorf("isolation = %q, want full", env["isolation"])
	}
	if len(env) != 2 {
		t.Errorf("env = %v, want exactly 2 fields", env)
	}
}

func TestReadUserSettingsMissingFileIsDefaults(t *testing.T) {
	us, err := ReadUserSettings(newMapFS(), "/nowhere/settings.json")
	if err != nil {
		t.Fatalf("missing file: %v", err)
	}
	if us.SchemaVersion != userSettingsSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", us.SchemaVersion, userSettingsSchemaVersion)
	}
	if us.DefaultRuntime != "" {
		t.Errorf("DefaultRuntime = %q, want empty", us.DefaultRuntime)
	}
}

func TestReadUserSettingsRoundTrip(t *testing.T) {
	fs := newMapFS()
	path := "/x/settings.json"
	in := UserSettings{DefaultRuntime: "tart", DefaultModel: "albert/x", CredentialRef: "main"}
	if err := WriteUserSettings(fs, path, in); err != nil {
		t.Fatalf("write: %v", err)
	}
	us, err := ReadUserSettings(fs, path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if us.DefaultRuntime != "tart" || us.DefaultModel != "albert/x" || us.CredentialRef != "main" {
		t.Errorf("round trip = %+v", us)
	}
	if us.SchemaVersion != userSettingsSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", us.SchemaVersion, userSettingsSchemaVersion)
	}
}

func TestReadUserSettingsNewerSchemaRejected(t *testing.T) {
	fs := newMapFS()
	fs.WriteFile("/x/settings.json", []byte(`{"schemaVersion": 99, "defaultRuntime": "tart"}`), 0o600)
	_, err := ReadUserSettings(fs, "/x/settings.json")
	if err == nil || !strings.Contains(err.Error(), "newer than this build supports") {
		t.Fatalf("err = %v, want unsupported-schema error", err)
	}
}

func TestReadUserSettingsMissingSchemaVersionRejected(t *testing.T) {
	fs := newMapFS()
	fs.WriteFile("/x/settings.json", []byte(`{"defaultRuntime": "tart"}`), 0o600)
	_, err := ReadUserSettings(fs, "/x/settings.json")
	if err == nil || !strings.Contains(err.Error(), "schemaVersion missing or invalid") {
		t.Fatalf("err = %v, want missing-schema error", err)
	}
}

func TestReadUserSettingsSecretFieldRejected(t *testing.T) {
	fs := newMapFS()
	fs.WriteFile("/x/settings.json", []byte(`{"schemaVersion": 1, "apiKey": "sk-literal"}`), 0o600)
	_, err := ReadUserSettings(fs, "/x/settings.json")
	if err == nil || !strings.Contains(err.Error(), "credentialRef") {
		t.Fatalf("err = %v, want secret-field rejection pointing at credentialRef", err)
	}
}

func TestReadProjectManifestSecretFieldRejected(t *testing.T) {
	fs := newMapFS()
	fs.files["/root/.codebox/project.json"] = []byte(`{"schemaVersion": 1, "token": "ghp_literal"}`)
	_, err := ReadProjectManifest(fs, "/root/.codebox/project.json")
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("err = %v, want secret-field rejection naming token", err)
	}
}

func TestReadLockfileSecretFieldRejected(t *testing.T) {
	fs := newMapFS()
	fs.files["/root/.codebox/lock.json"] = []byte(`{"schemaVersion": 3, "entries": {}, "token": "ghp_literal"}`)
	_, err := ReadLockfile(fs, "/root/.codebox/lock.json")
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("err = %v, want secret-field rejection naming token", err)
	}
}

func TestProjectManifestMCPSelectionBumpsSchemaAndReadsPreviousVersion(t *testing.T) {
	fs := newMapFS()
	path := "/root/.codebox/project.json"
	fs.files[path] = []byte(`{"schemaVersion":2,"mcpConnectors":["context7"]}`)
	manifest, err := ReadProjectManifest(fs, path)
	if err != nil || manifest.SchemaVersion != 2 || len(manifest.MCPConnectors) != 1 || manifest.MCPConnectors[0] != "context7" {
		t.Fatalf("previous schema selection = %+v, %v", manifest, err)
	}
	if err := WriteProjectManifest(fs, path, manifest); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(fs.files[path]), `"schemaVersion": 7`) {
		t.Fatalf("updated manifest did not write schema version 7: %s", fs.files[path])
	}
	// A schemaVersion 7 manifest with the optional secrets block is readable.
	fs.files[path] = []byte(`{"schemaVersion":7,"secrets":{"projectId":"22222222-2222-4222-8222-222222222222","bindings":[]}}`)
	manifest, err = ReadProjectManifest(fs, path)
	if err != nil || manifest.Secrets == nil || manifest.Secrets.ProjectID != "22222222-2222-4222-8222-222222222222" {
		t.Fatalf("schema 7 secrets manifest = %+v, %v", manifest, err)
	}
	fs.files[path] = []byte(`{"schemaVersion":8}`)
	if _, err := ReadProjectManifest(fs, path); err == nil || !strings.Contains(err.Error(), "newer than this build supports") {
		t.Fatalf("unsupported future manifest schema error = %v", err)
	}
}

func TestReadProjectManifestMissingIsNotExists(t *testing.T) {
	_, err := ReadProjectManifest(newMapFS(), "/root/.codebox/project.json")
	if !os.IsNotExist(err) {
		t.Fatalf("err = %v, want os.ErrNotExist-shaped error", err)
	}
}

func TestReadLockfileMissingIsEmpty(t *testing.T) {
	lf, err := ReadLockfile(newMapFS(), "/root/.codebox/lock.json")
	if err != nil {
		t.Fatalf("missing lock: %v", err)
	}
	if lf.SchemaVersion != lockfileSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", lf.SchemaVersion, lockfileSchemaVersion)
	}
}

func TestReadLockfileNewerSchemaRejected(t *testing.T) {
	fs := newMapFS()
	fs.files["/root/.codebox/lock.json"] = []byte(`{"schemaVersion": 4, "entries": {}}`)
	_, err := ReadLockfile(fs, "/root/.codebox/lock.json")
	if err == nil || !strings.Contains(err.Error(), "newer than this build supports") {
		t.Fatalf("err = %v, want unsupported-schema error", err)
	}
}

func TestAtomicWriteLeavesNoTempOnRenameFailure(t *testing.T) {
	fs := newMapFS()
	path := "/x/.codebox/project.json"
	// Seed an existing manifest, then fail the rename of the temp file the
	// next write creates. atomicWrite removes the temp on rename failure,
	// and the original target must survive intact.
	if err := WriteProjectManifest(fs, path, ProjectManifest{Project: "first"}); err != nil {
		t.Fatalf("initial write: %v", err)
	}
	original := string(fs.files[path])
	fs.failRename = true
	err := WriteProjectManifest(fs, path, ProjectManifest{Project: "second"})
	if err == nil {
		t.Fatal("write succeeded despite rename failure")
	}
	if string(fs.files[path]) != original {
		t.Errorf("original target modified by failed write: %q", fs.files[path])
	}
	for name := range fs.files {
		if strings.Contains(name, ".tmp-") {
			t.Errorf("leftover temp file %s after failed rename", name)
		}
	}
}

func TestProjectPathsUsePlatformHelpers(t *testing.T) {
	root := string(filepath.Separator) + "proj"
	if ProjectManifestPath(root) != filepath.Join(root, ".codebox", "project.json") {
		t.Errorf("ProjectManifestPath = %q", ProjectManifestPath(root))
	}
	if ProjectLockPath(root) != filepath.Join(root, ".codebox", "lock.json") {
		t.Errorf("ProjectLockPath = %q", ProjectLockPath(root))
	}
}

func TestExplainRedactsNothingBecauseItNeverSeesSecrets(t *testing.T) {
	// Explain reads only managed fields; a credential literal can never
	// enter through the managed schema (rejected at parse). The test
	// proves the output contains only the reference name.
	s := Settings{
		User: map[string]string{"credential_ref": "main"},
	}
	out := FormatExplain(Explain(s))
	if strings.Contains(out, "sk-") || strings.Contains(out, "ghp_") {
		t.Errorf("explain output leaked a secret: %q", out)
	}
	if !strings.Contains(out, "main") {
		t.Errorf("explain output missing credential_ref value: %q", out)
	}
}

func TestExplainShowsSourcePerField(t *testing.T) {
	s := Settings{
		Env:     map[string]string{"isolation": "full"},
		Project: map[string]string{"model": "albert/x"},
	}
	out := FormatExplain(Explain(s))
	if !strings.Contains(out, "isolation") || !strings.Contains(out, "full") {
		t.Errorf("explain missing isolation: %q", out)
	}
	if !strings.Contains(out, "model") {
		t.Errorf("explain missing model: %q", out)
	}
}

func TestImportLegacyDotenvMapsRecognizedKeys(t *testing.T) {
	imp := ImportLegacyDotenv("RUNTIME=tart\nISOLATION=full\nWORKSPACE_DIR=/tmp/p\n")
	if imp.Mapped["runtime"] != "tart" {
		t.Errorf("runtime = %q, want tart", imp.Mapped["runtime"])
	}
	if imp.Mapped["isolation"] != "full" {
		t.Errorf("isolation = %q, want full", imp.Mapped["isolation"])
	}
	if imp.Mapped["workspace_dir"] != "/tmp/p" {
		t.Errorf("workspace_dir = %q, want /tmp/p", imp.Mapped["workspace_dir"])
	}
	if len(imp.Unrecognized) != 0 {
		t.Errorf("Unrecognized = %v, want empty", imp.Unrecognized)
	}
}

func TestImportLegacyDotenvListsVariableNamesWithoutValues(t *testing.T) {
	imp := ImportLegacyDotenv("RUNTIME=tart\nCONTEXT7_API_KEY=context-secret\nUNKNOWN=value\nRUNTIME=other\n")
	if got, want := strings.Join(imp.Keys, ","), "CONTEXT7_API_KEY,RUNTIME,UNKNOWN"; got != want {
		t.Fatalf("Keys = %q, want %q", got, want)
	}
	if strings.Contains(strings.Join(imp.Keys, " "), "secret") || strings.Contains(strings.Join(imp.Keys, " "), "value") {
		t.Fatal("legacy key summary contains dotenv values")
	}
}

func TestImportLegacyDotenvCredentialIsAdviceNotValue(t *testing.T) {
	imp := ImportLegacyDotenv("ALBERT_API_KEY=sk-super-secret\nSOME_TOKEN=abc\n")
	if len(imp.Mapped) != 0 {
		t.Errorf("Mapped = %v, want empty (credentials never mapped)", imp.Mapped)
	}
	out := imp.FormatLegacyImport()
	if strings.Contains(out, "sk-super-secret") || strings.Contains(out, "abc") {
		t.Errorf("preview leaked secret bytes: %q", out)
	}
	if !strings.Contains(out, "credential store") {
		t.Errorf("preview missing credential advice: %q", out)
	}
}

func TestImportLegacyDotenvUnrecognizedReported(t *testing.T) {
	imp := ImportLegacyDotenv("SOME_RANDOM_FLAG=1\nANOTHER=2\n")
	if len(imp.Mapped) != 0 {
		t.Errorf("Mapped = %v, want empty", imp.Mapped)
	}
	if len(imp.Unrecognized) != 2 {
		t.Errorf("Unrecognized = %v, want 2 entries", imp.Unrecognized)
	}
}

func TestImportLegacyDotenvCredentialShapedUnknownKeyIsAdvice(t *testing.T) {
	// An unknown variable whose name looks like a credential must not be
	// mapped or echoed, only advised.
	imp := ImportLegacyDotenv("MY_SERVICE_SECRET=hunter2\n")
	if len(imp.Mapped) != 0 {
		t.Errorf("Mapped = %v, want empty", imp.Mapped)
	}
	if len(imp.Unrecognized) != 0 {
		t.Errorf("Unrecognized = %v, want empty (credential-shaped keys are advice, not unrecognized)", imp.Unrecognized)
	}
	if strings.Contains(imp.FormatLegacyImport(), "hunter2") {
		t.Error("preview leaked credential-shaped value")
	}
}

func TestImportLegacyDotenvDuplicateFirstWins(t *testing.T) {
	imp := ImportLegacyDotenv("RUNTIME=tart\nRUNTIME=agent-vm\n")
	if imp.Mapped["runtime"] != "tart" {
		t.Errorf("runtime = %q, want first definition (tart)", imp.Mapped["runtime"])
	}
}

func TestExplainShowsDefaultsAndExplicitEmptyWins(t *testing.T) {
	// P04 Codex fixes: unset fields print their built-in default (source
	// "default"), and an explicit empty project value wins over a user
	// value — it means "turn this field off", not "absent".
	out := FormatExplain(Explain(Settings{
		Project: map[string]string{"model": ""},
		User:    map[string]string{"model": "user-model"},
	}))
	if !strings.Contains(out, "runtime") || !strings.Contains(out, "microsandbox") {
		t.Errorf("default runtime must be visible: %q", out)
	}
	if !strings.Contains(out, "workspace_dir") || !strings.Contains(out, "./workspace") {
		t.Errorf("default workspace_dir must be visible: %q", out)
	}
	// The explicit project empty wins over the user value.
	s := Settings{
		Project: map[string]string{"model": ""},
		User:    map[string]string{"model": "user-model"},
	}
	if got := s.setting("model"); !got.Set || got.Value != "" || got.source != SourceProject {
		t.Errorf("explicit empty must win: %+v", got)
	}
}

func TestExplainFlagIsHighestPrecedence(t *testing.T) {
	s := Settings{
		Flag:    map[string]string{"runtime": "tart"},
		Env:     map[string]string{"runtime": "microsandbox"},
		Project: map[string]string{"runtime": "agent-vm"},
	}
	if got := s.setting("runtime"); got.Value != "tart" || got.source != SourceFlag {
		t.Errorf("flag must win: %+v", got)
	}
}
