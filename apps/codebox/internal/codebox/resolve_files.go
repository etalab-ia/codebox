package codebox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Readers and writers for the three managed file formats (settings.json,
// project.json, lock.json). All writes are atomic: the new content lands in
// a temp file next to the target, then is renamed over it, so an interrupted
// write can never leave a half-written managed file behind.

// FS is the filesystem surface the readers/writers use, so tests can inject
// an in-memory FS without touching the real one.
type FS interface {
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte, perm os.FileMode) error
	MkdirAll(path string, perm os.FileMode) error
	Remove(path string) error
	RenameTmp(oldPath, newPath string) error
	// WriteTemp writes data to a fresh temporary file in dir (named with
	// base as a prefix) and returns its path. It exists so atomic writes
	// stay inside the injected FS instead of touching the real disk.
	WriteTemp(dir, base string, data []byte, perm os.FileMode) (string, error)
	// CreateExclusive creates path with its content, failing with
	// os.ErrExist when it already exists. It is the exclusive-create
	// primitive used by per-project locks: the OS, not a check-then-write
	// sequence, guarantees exclusivity.
	CreateExclusive(path string, data []byte, perm os.FileMode) error
}

type realFS struct{}

func (realFS) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }
func (realFS) WriteFile(path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, data, perm)
}
func (realFS) MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }
func (realFS) Remove(path string) error                     { return os.Remove(path) }
func (realFS) RenameTmp(oldPath, newPath string) error      { return os.Rename(oldPath, newPath) }
func (realFS) WriteTemp(dir, base string, data []byte, perm os.FileMode) (string, error) {
	tmp, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return "", err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		os.Remove(name)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

func (realFS) CreateExclusive(path string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	return f.Close()
}

// DefaultFS is the real filesystem.
var DefaultFS FS = realFS{}

// ReadUserSettings reads and validates the global settings file. A missing
// file is not an error: it yields the zero settings (all defaults). A file
// with an unsupported schemaVersion or a secret-suggestive field is a hard
// error.
func ReadUserSettings(fs FS, path string) (UserSettings, error) {
	data, err := fs.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return UserSettings{SchemaVersion: userSettingsSchemaVersion}, nil
		}
		return UserSettings{}, fmt.Errorf("read user settings: %w", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return UserSettings{}, fmt.Errorf("user settings %s: invalid JSON: %w", path, err)
	}
	if err := checkNoSecretFields("user settings", raw); err != nil {
		return UserSettings{}, err
	}
	var us UserSettings
	if err := json.Unmarshal(data, &us); err != nil {
		return UserSettings{}, fmt.Errorf("user settings %s: %w", path, err)
	}
	if err := checkSchemaVersion("user settings", us.SchemaVersion, maxSupportedSettingsSchema); err != nil {
		return UserSettings{}, err
	}
	return us, nil
}

// ReadProjectManifest reads and validates a project manifest. A missing file
// is an error for the caller to interpret (no project configured yet), so it
// is returned as a distinguishable not-exist error.
func ReadProjectManifest(fs FS, path string) (ProjectManifest, error) {
	data, err := fs.ReadFile(path)
	if err != nil {
		return ProjectManifest{}, err
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return ProjectManifest{}, fmt.Errorf("project manifest %s: invalid JSON: %w", path, err)
	}
	if err := checkNoSecretFields("project manifest", raw); err != nil {
		return ProjectManifest{}, err
	}
	var pm ProjectManifest
	if err := json.Unmarshal(data, &pm); err != nil {
		return ProjectManifest{}, fmt.Errorf("project manifest %s: %w", path, err)
	}
	if err := checkSchemaVersion("project manifest", pm.SchemaVersion, maxSupportedManifestSchema); err != nil {
		return ProjectManifest{}, err
	}
	if err := ValidateProjectSecretsManifest(pm.Secrets); err != nil {
		return ProjectManifest{}, fmt.Errorf("project manifest %s: %w", path, err)
	}
	return pm, nil
}

// ReadLockfile reads and validates a project lockfile. A missing lockfile is
// not an error: a project may not have resolved anything yet.
func ReadLockfile(fs FS, path string) (Lockfile, error) {
	data, err := fs.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Lockfile{SchemaVersion: lockfileSchemaVersion}, nil
		}
		return Lockfile{}, err
	}
	var lf Lockfile
	if err := json.Unmarshal(data, &lf); err != nil {
		return Lockfile{}, fmt.Errorf("lockfile %s: %w", path, err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return Lockfile{}, fmt.Errorf("lockfile %s: invalid JSON: %w", path, err)
	}
	if err := checkNoSecretFields("project lockfile", raw); err != nil {
		return Lockfile{}, err
	}
	if err := checkSchemaVersion("lockfile", lf.SchemaVersion, maxSupportedLockfileSchema); err != nil {
		return Lockfile{}, err
	}
	return lf, nil
}

// ValidateDependencySet rejects a manifest/lock pair split by an interrupted
// update. Legacy pairs with no generation remain readable.
func ValidateDependencySet(pm ProjectManifest, lf Lockfile) error {
	if pm.DependencySetID == "" && lf.DependencySetID == "" {
		return nil
	}
	if !isUpdateID(pm.DependencySetID) || pm.DependencySetID != lf.DependencySetID {
		return fmt.Errorf("project manifest and lockfile belong to different dependency sets; repair the pair before launch")
	}
	return nil
}

// atomicWrite writes data to path via a temp file + rename, creating parent
// directories first. The temp file lives in the same directory so rename
// stays on one filesystem. All filesystem access goes through fs, so an
// injected FS sees the temp file and the rename.
func atomicWrite(fs FS, path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := fs.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	name, err := fs.WriteTemp(dir, filepath.Base(path), data, perm)
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := fs.RenameTmp(name, path); err != nil {
		_ = fs.Remove(name)
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// WriteUserSettings atomically writes the global settings file.
func WriteUserSettings(fs FS, path string, us UserSettings) error {
	us.SchemaVersion = userSettingsSchemaVersion
	data, err := json.MarshalIndent(us, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(fs, path, append(data, '\n'), 0o644)
}

// WriteProjectManifest atomically writes the project manifest.
func WriteProjectManifest(fs FS, path string, pm ProjectManifest) error {
	pm.SchemaVersion = projectManifestSchemaVersion
	data, err := json.MarshalIndent(pm, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(fs, path, append(data, '\n'), 0o644)
}

// WriteProjectPorts updates only the port list, preserving manifest fields
// this version does not yet understand.
func WriteProjectPorts(fs FS, path string, ports []PortMapping) error {
	data, err := fs.ReadFile(path)
	if err != nil {
		return err
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("project manifest %s: invalid JSON: %w", path, err)
	}
	if err := checkNoSecretFields("project manifest", raw); err != nil {
		return err
	}
	var pm ProjectManifest
	if err := json.Unmarshal(data, &pm); err != nil {
		return fmt.Errorf("project manifest %s: %w", path, err)
	}
	if err := checkSchemaVersion("project manifest", pm.SchemaVersion, maxSupportedManifestSchema); err != nil {
		return err
	}
	if ports == nil {
		ports = []PortMapping{}
	}
	raw["schemaVersion"] = projectManifestSchemaVersion
	raw["ports"] = ports
	raw["portsConfigured"] = true
	updated, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(fs, path, append(updated, '\n'), 0o644)
}

// WriteLockfile atomically writes the project lockfile.
func WriteLockfile(fs FS, path string, lf Lockfile) error {
	lf.SchemaVersion = lockfileSchemaVersion
	data, err := json.MarshalIndent(lf, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(fs, path, append(data, '\n'), 0o644)
}
