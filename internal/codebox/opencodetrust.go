package codebox

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// This file implements the P10 host-local trust record for
// execution-relevant project inputs. OpenCode loads project plugins and MCP
// commands as executable code at config load (D-001) and Pi runs the project's
// own extensions and settings at launch (#176 review), so a cloned repository
// can execute code on first start without any declaration. Approval is a
// host-local decision keyed by the canonical project and the CONTENT hash
// of the approved inputs: a changed file is unapproved again until the user
// re-approves. The record never travels in the repository.

// TrustRecord is the persisted per-project approval of execution inputs.
type TrustRecord struct {
	SchemaVersion int `json:"schemaVersion"`
	// Project is the canonical project root (absolute, symlink-resolved).
	Project string `json:"project"`
	// Approved lists one entry per approved input, as "path:sha256" — the
	// path relative to the project root and the content hash at approval
	// time. A mismatch on recheck means the input changed: approval is stale
	// and codebox must refuse to run it without a new approval.
	Approved []string `json:"approved,omitempty"`
}

const trustRecordSchemaVersion = 1

// opencodeTrustPath is the trust record location for a project root, in
// host state (never in the repository).
func opencodeTrustPath(stateDir, projectRoot string) string {
	return filepath.Join(stateDir, "projects", safeFileName(projectRoot), "opencode-trust.json")
}

// OpenCodeTrustPath exposes the trust record path for the CLI.
func OpenCodeTrustPath(stateDir, projectRoot string) string {
	return opencodeTrustPath(stateDir, projectRoot)
}

// ReadTrustRecord loads the record for a project root. A missing file is
// not an error: nothing is approved.
func ReadTrustRecord(fs FS, stateDir, projectRoot string) (TrustRecord, error) {
	data, err := fs.ReadFile(opencodeTrustPath(stateDir, projectRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return TrustRecord{SchemaVersion: trustRecordSchemaVersion, Project: projectRoot}, nil
		}
		return TrustRecord{}, err
	}
	var r TrustRecord
	if err := json.Unmarshal(data, &r); err != nil {
		return TrustRecord{}, fmt.Errorf("trust record %s: %w", opencodeTrustPath(stateDir, projectRoot), err)
	}
	if r.SchemaVersion > trustRecordSchemaVersion {
		return TrustRecord{}, fmt.Errorf("trust record %s: schemaVersion %d is newer than this build supports (%d)",
			opencodeTrustPath(stateDir, projectRoot), r.SchemaVersion, trustRecordSchemaVersion)
	}
	return r, nil
}

// WriteTrustRecord atomically persists the record.
func WriteTrustRecord(fs FS, stateDir, projectRoot string, r TrustRecord) error {
	r.SchemaVersion = trustRecordSchemaVersion
	r.Project = projectRoot
	approved := append([]string(nil), r.Approved...)
	sort.Strings(approved)
	r.Approved = approved
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := fs.MkdirAll(filepath.Dir(opencodeTrustPath(stateDir, projectRoot)), 0o755); err != nil {
		return err
	}
	return atomicWrite(fs, opencodeTrustPath(stateDir, projectRoot), append(data, '\n'), 0o600)
}

// hashFileContent returns the sha256 of the file at rel under root,
// hex-encoded. Every path component below root must be symlink-free
// (confinedProjectPath), and the final file must be a regular file: an
// approval record must pin actual content, not a path that can be repointed
// after approval — including via a symlinked parent directory repointed at
// the outside world — and a non-regular file (FIFO, device) could hang or
// stream unbounded data into the hash. The content is hashed by streaming so
// a large regular file never loads into memory.
func hashFileContent(root, rel string) (string, error) {
	path, err := confinedProjectPath(root, rel)
	if err != nil {
		return "", err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%s is a symlink; refusing to approve a repointable path", path)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file; refusing to hash it", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(fi, opened) {
		return "", fmt.Errorf("%s changed while opening; refusing to hash it", path)
	}
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// UnapprovedInputs computes the execution-relevant inputs of a project that
// are NOT covered by the current trust record: unapproved files, and
// approved files whose content hash no longer matches (the recheck the plan
// requires — approval is tied to content, and a changed file is a new
// decision). Config-declared entries (plugins, MCP commands) hash the whole
// config file to pin their declarations. Local plugin files are also hashed
// individually, so editing code inside an approved plugin requires fresh review.
type UnapprovedInput struct {
	// Path is the input's path relative to the project root.
	Path string
	// Reason is "unapproved" or "changed".
	Reason string
	// Hash pins the content presented for interactive review. Empty is unreadable.
	Hash string
}

// ExecutionInputPaths returns every project file whose content executes or
// launches a process, as paths relative to root. Two classes feed it: the
// config files that carry executable declarations — the OpenCode config when it
// declares plugins or MCP entries (its own hash then covers every declaration
// inside it), and the project-local Pi settings, which the review on #345
// showed can declare extensions and packages — and the files that run without
// any declaration at all, whether auto-discovered (OpenCode plugins, Pi
// extensions) or reached through an explicit local declaration (an OpenCode
// plugin path, a Pi settings "extensions" entry). A file can be both declared
// and auto-discovered; trust review and approval must contain it only once.
func (inputs ProjectExecutionInputs) ExecutionInputPaths(root string) []string {
	seen := map[string]bool{}
	var paths []string
	add := func(rel string) {
		if rel == "" || seen[rel] {
			return
		}
		seen[rel] = true
		paths = append(paths, rel)
	}
	if inputs.ConfigPath != "" && (len(inputs.Plugins) > 0 || len(inputs.MCPCommands) > 0 || len(inputs.MCPEndpoints) > 0) {
		if rel, err := filepath.Rel(root, inputs.ConfigPath); err == nil {
			add(rel)
		}
	}
	if inputs.PiProjectConfig != "" {
		if rel, err := filepath.Rel(root, inputs.PiProjectConfig); err == nil {
			add(rel)
		}
	}
	for _, rel := range inputs.AutoDiscoveredPlugins {
		add(rel)
	}
	for _, rel := range inputs.DeclaredPluginFiles {
		add(rel)
	}
	for _, rel := range inputs.PiAutoDiscoveredExtensions {
		add(rel)
	}
	for _, rel := range inputs.PiDeclaredCodeFiles {
		add(rel)
	}
	sort.Strings(paths)
	return paths
}

func DiscoverUnapprovedInputs(fs FS, stateDir, projectRoot string) ([]UnapprovedInput, error) {
	inputs, err := DiscoverProjectExecutionInputs(projectRoot)
	if err != nil {
		return nil, err
	}
	record, err := ReadTrustRecord(fs, stateDir, projectRoot)
	if err != nil {
		return nil, err
	}
	approved := map[string]string{}
	for _, entry := range record.Approved {
		path, hash, ok := strings.Cut(entry, ":")
		if ok {
			approved[path] = hash
		}
	}
	var out []UnapprovedInput
	check := func(rel string) {
		hash, err := hashFileContent(projectRoot, rel)
		if err != nil {
			out = append(out, UnapprovedInput{Path: rel, Reason: "unreadable: " + err.Error()})
			return
		}
		prior, was := approved[rel]
		switch {
		case !was:
			out = append(out, UnapprovedInput{Path: rel, Reason: "unapproved", Hash: hash})
		case prior != hash:
			out = append(out, UnapprovedInput{Path: rel, Reason: "changed since approval", Hash: hash})
		}
	}
	for _, rel := range inputs.ExecutionInputPaths(projectRoot) {
		check(rel)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// ApproveReviewedInputs approves only the hashes shown before the prompt, never
// newly discovered content. Changed or added inputs still fail the launch gate.
func ApproveReviewedInputs(fs FS, stateDir, root string, reviewed []UnapprovedInput) error {
	record, err := ReadTrustRecord(fs, stateDir, root)
	if err != nil {
		return err
	}
	for _, input := range reviewed {
		hash, err := hashFileContent(root, input.Path)
		if err != nil || input.Hash == "" || hash != input.Hash {
			return fmt.Errorf("%s a changé ou ne peut pas être lu ; relancez la vérification", input.Path)
		}
	}
	for _, input := range reviewed {
		prefix := input.Path + ":"
		kept := record.Approved[:0]
		for _, entry := range record.Approved {
			if !strings.HasPrefix(entry, prefix) {
				kept = append(kept, entry)
			}
		}
		record.Approved = append(kept, prefix+input.Hash)
	}
	if err := WriteTrustRecord(fs, stateDir, root, record); err != nil {
		return err
	}
	remaining, err := DiscoverUnapprovedInputs(fs, stateDir, root)
	if err != nil {
		return err
	}
	if len(remaining) > 0 {
		return fmt.Errorf("les entrées du projet ont changé ; relancez la vérification")
	}
	return nil
}

// ApproveExecutionInputs records approval for every currently discovered
// execution input, at its current content hash. The project config (when it
// carries plugins or MCP commands) is approved with its own hash.
func ApproveExecutionInputs(fs FS, stateDir, projectRoot string) error {
	inputs, err := DiscoverProjectExecutionInputs(projectRoot)
	if err != nil {
		return err
	}
	record, err := ReadTrustRecord(fs, stateDir, projectRoot)
	if err != nil {
		return err
	}
	// Start from the currently discovered inputs, not the old record:
	// approvals for files that no longer exist are dropped (a deleted
	// plugin's approval must not survive to re-approve a re-created file
	// of the same name with different content).
	live := map[string]bool{}
	for _, rel := range inputs.ExecutionInputPaths(projectRoot) {
		live[rel] = true
	}
	keep := map[string]string{}
	for _, entry := range record.Approved {
		path, hash, ok := strings.Cut(entry, ":")
		if ok && live[path] {
			keep[path] = hash
		}
	}
	add := func(rel string) error {
		hash, err := hashFileContent(projectRoot, rel)
		if err != nil {
			return fmt.Errorf("approve %s: %w", rel, err)
		}
		keep[rel] = hash
		return nil
	}
	for _, rel := range inputs.ExecutionInputPaths(projectRoot) {
		if err := add(rel); err != nil {
			return err
		}
	}
	entries := make([]string, 0, len(keep))
	for path, hash := range keep {
		entries = append(entries, path+":"+hash)
	}
	record.Approved = entries
	return WriteTrustRecord(fs, stateDir, projectRoot, record)
}
