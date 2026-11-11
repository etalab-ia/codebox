package codebox

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// This file reads the OpenCode project configuration, read-only. Per D-001:
// JSONC is discovered at four locations, comments are legal, and the file is
// never rewritten. codebox parses it only to detect managed-field
// conflicts and execution-relevant inputs (plugins, MCP servers).

// opencodeConfigFiles lists the project config locations in OpenCode's
// discovery order, relative to the project root.
var opencodeConfigFiles = []string{
	"opencode.json",
	"opencode.jsonc",
	".opencode/opencode.json",
	".opencode/opencode.jsonc",
}

// confinedProjectPath verifies that no component of rel below root is a
// symlink or a parent traversal, and returns the joined path. Lstat and Open
// both resolve intermediate symlinks, so an untrusted checkout that makes
// .opencode (or any deeper directory) a symlink would otherwise make project
// reads touch files outside the checkout. The walk starts at root, not at
// the filesystem root: absolute paths legitimately contain symlinks (on
// macOS every /tmp and /var path is one), so only the project-relative
// components are checked.
func confinedProjectPath(root, rel string) (string, error) {
	rel = filepath.Clean(rel)
	if rel == "." {
		return root, nil
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("project path %s must be relative to the project root", rel)
	}
	cur := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			return "", fmt.Errorf("project path %s escapes the project root", rel)
		}
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if err != nil {
			return "", err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%s is a symlink; refusing to follow project paths outside the checkout", cur)
		}
	}
	return cur, nil
}

// maxOpenCodeConfigSize bounds a single project OpenCode config read. The
// config is read from checkouts that may be untrusted (import preview, trust
// discovery), so the read is bounded the same way the importer bounds its
// inputs.
const maxOpenCodeConfigSize = 1 << 20

// FindProjectOpenCodeConfig returns the first existing OpenCode config path
// under root, or "" when none exists. Discovery order matches OpenCode's
// (files at the root before files in .opencode/). It uses Lstat and reports
// any non-directory entry, including symlinks and non-regular files: the
// config must stay in the trust flow so a repointable or unreadable entry
// blocks launch (parse and approval both refuse it) instead of silently
// disappearing from discovery.
func FindProjectOpenCodeConfig(root string) string {
	for _, rel := range opencodeConfigFiles {
		path := filepath.Join(root, rel)
		if fi, err := os.Lstat(path); err == nil && !fi.IsDir() {
			return path
		}
	}
	return ""
}

// stripJSONCComments removes // line comments and /* */ block comments that
// are outside string literals, so JSONC can be parsed as JSON. Trailing
// commas are also removed. The transformation is intentionally conservative:
// comment markers inside string values are preserved (the scanner tracks
// string state), and anything it cannot classify is left in place for the
// JSON parser to reject loudly rather than silently misparse.
func stripJSONCComments(src string) string {
	var b strings.Builder
	inString := false
	escaped := false
	i := 0
	for i < len(src) {
		c := src[i]
		if inString {
			b.WriteByte(c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			i++
			continue
		}
		switch {
		case c == '"':
			inString = true
			b.WriteByte(c)
			i++
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			// Line comment: skip to the newline.
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			// Block comment: skip to the closing marker.
			start := i
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			if i+1 >= len(src) {
				b.WriteString(src[start:])
				i = len(src)
				break
			}
			i += 2
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// stripTrailingCommas removes commas that directly precede a closing brace
// or bracket (outside strings), which JSONC permits and JSON rejects.
func stripTrailingCommas(src string) string {
	var b strings.Builder
	inString := false
	escaped := false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inString {
			b.WriteByte(c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		if c == ',' {
			// Look ahead past whitespace for a closer.
			j := i + 1
			for j < len(src) && (src[j] == ' ' || src[j] == '\t' || src[j] == '\n' || src[j] == '\r') {
				j++
			}
			if j < len(src) && (src[j] == '}' || src[j] == ']') {
				continue // drop the comma
			}
		}
		if c == '"' {
			inString = true
		}
		b.WriteByte(c)
	}
	return b.String()
}

// ParseProjectOpenCodeConfig reads and parses the config at path (JSON or
// JSONC) into a generic map, confining the read to root: any symlink in the
// path components below root is refused, not followed. It is strictly
// read-only. An empty path returns an empty map and no error (no project
// config is a normal state). A malformed file is an error: the conflict
// detector must not silently treat an unparseable project config as "no
// conflicts".
func ParseProjectOpenCodeConfig(root, path string) (map[string]any, error) {
	if path == "" {
		return map[string]any{}, nil
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return nil, fmt.Errorf("read project OpenCode config %s: %w", path, err)
	}
	abs, err := confinedProjectPath(root, rel)
	if err != nil {
		return nil, fmt.Errorf("read project OpenCode config %s: %w", path, err)
	}
	raw, err := readProjectConfigFile(abs)
	if err != nil {
		return nil, fmt.Errorf("read project OpenCode config %s: %w", path, err)
	}
	parsed, err := parseProjectOpenCodeConfig(raw)
	if err != nil {
		return nil, fmt.Errorf("parse project OpenCode config %s: %w", path, err)
	}
	return parsed, nil
}

// readProjectConfigFile reads a project OpenCode config without following
// symlinks, without opening non-regular files, and with a size bound. The
// config is read from checkouts that may be untrusted, so a symlink pointing
// outside the worktree, a FIFO that would hang the read, or a device file
// that would exhaust memory must be refused instead of read.
func readProjectConfigFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file; refusing to read")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, fmt.Errorf("file changed while opening; refusing to read")
	}
	if openedInfo.Size() > maxOpenCodeConfigSize {
		return nil, fmt.Errorf("exceeds the OpenCode config size limit (%d bytes)", maxOpenCodeConfigSize)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxOpenCodeConfigSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxOpenCodeConfigSize {
		return nil, fmt.Errorf("exceeds the OpenCode config size limit (%d bytes)", maxOpenCodeConfigSize)
	}
	return data, nil
}

func parseProjectOpenCodeConfig(raw []byte) (map[string]any, error) {
	cleaned := stripTrailingCommas(stripJSONCComments(string(raw)))
	var parsed map[string]any
	if err := json.Unmarshal([]byte(cleaned), &parsed); err != nil {
		return nil, fmt.Errorf("decode JSONC: %w", err)
	}
	if parsed == nil {
		return nil, fmt.Errorf("decode JSONC: expected a JSON object")
	}
	return parsed, nil
}

// ProjectExecutionInputs is the set of project-declared, execution-relevant
// inputs codebox must gate behind host-local trust approval before a harness
// loads them. Per D-001: declared AND auto-discovered OpenCode plugins execute
// arbitrary code at config load, and MCP server commands launch host processes
// from the project config. Pi runs the project's own extensions and settings at
// launch, the same way (#176 review).
type ProjectExecutionInputs struct {
	// ConfigPath is the project OpenCode config path, when one exists.
	ConfigPath string
	// Plugins lists project plugin declarations (the "plugin" field values
	// from the config).
	Plugins []string
	// DeclaredPluginFiles lists regular files reachable through explicit local
	// plugin file/directory declarations. Their content needs approval too:
	// hashing only the declaring config would miss later code changes.
	DeclaredPluginFiles []string
	// AutoDiscoveredPlugins lists .js/.ts files under .opencode/plugin/
	// and .opencode/plugins/ — they execute without any declaration.
	AutoDiscoveredPlugins []string
	// PiProjectConfig is the project-local .pi/settings.json, when one
	// exists. Pi reads it in preference to the managed global settings, and
	// its schema can declare further extensions and packages, so the file is
	// an execution input in the same sense the OpenCode config is.
	PiProjectConfig string
	// PiAutoDiscoveredExtensions lists the files under .pi/extensions/ — Pi
	// loads and runs them with the guest's permissions at launch, without any
	// declaration. Gated on every start path, not only under the Pi harness:
	// the harness is recorded in the repository-controlled project manifest,
	// so a cloned repository can select Pi on its own.
	PiAutoDiscoveredExtensions []string
	// PiDeclaredCodeFiles lists the regular files reachable through the local
	// path declarations of the settings file: the "extensions" array, and the
	// "packages" entries that name a local file or directory rather than an
	// npm or git dependency. They execute at launch like the auto-discovered
	// ones, so they need their own content approval: the settings hash would
	// otherwise pin the declaration while a later change to the declared file
	// slipped through unreviewed.
	PiDeclaredCodeFiles []string
	// MCPCommands lists the "command" of every mcp server entry declared in
	// the project config (local servers only; remote URLs are endpoints,
	// gated by the endpoint check instead).
	MCPCommands []string
	// MCPEndpoints lists the URL of every remote MCP server declared in the
	// project config. The config content hash makes endpoint changes require
	// fresh host-local approval.
	MCPEndpoints []string
}

// DiscoverProjectExecutionInputs inspects the project root for the inputs
// that execute code or launch processes when OpenCode loads. The config is
// parsed read-only; plugin files are listed, never executed.
func DiscoverProjectExecutionInputs(root string) (ProjectExecutionInputs, error) {
	out := ProjectExecutionInputs{}
	cfgPath := FindProjectOpenCodeConfig(root)
	out.ConfigPath = cfgPath
	if cfgPath != "" {
		cfg, err := ParseProjectOpenCodeConfig(root, cfgPath)
		if err != nil {
			return out, err
		}
		for _, key := range []string{"plugin", "plugins"} {
			if plugins, ok := cfg[key].([]any); ok {
				for _, p := range plugins {
					s, _ := p.(string)
					if entry, ok := p.(map[string]any); ok {
						s, _ = entry["package"].(string)
					}
					if s != "" {
						out.Plugins = append(out.Plugins, s)
						files, err := declaredPluginFiles(root, cfgPath, s)
						if err != nil {
							return out, fmt.Errorf("inspect declared plugin %q: %w", s, err)
						}
						out.DeclaredPluginFiles = append(out.DeclaredPluginFiles, files...)
					}
				}
			}
		}
		if mcp, ok := cfg["mcp"].(map[string]any); ok {
			servers := []map[string]any{mcp}
			if native, ok := mcp["servers"].(map[string]any); ok {
				servers = append(servers, native)
			}
			for _, entries := range servers {
				for _, server := range entries {
					m, ok := server.(map[string]any)
					if !ok {
						continue
					}
					// Local servers carry a command; remote servers carry a URL.
					if cmd, ok := m["command"].(string); ok && cmd != "" {
						out.MCPCommands = append(out.MCPCommands, cmd)
					}
					if argv, ok := m["command"].([]any); ok && len(argv) > 0 {
						encoded, _ := json.Marshal(argv)
						out.MCPCommands = append(out.MCPCommands, string(encoded))
					}
					if endpoint, ok := m["url"].(string); ok && endpoint != "" {
						out.MCPEndpoints = append(out.MCPEndpoints, endpoint)
					}
				}
			}
		}
	}
	for _, dir := range []string{".opencode/plugin", ".opencode/plugins"} {
		abs, err := confinedProjectPath(root, dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return out, fmt.Errorf("read %s: %w", dir, err)
		}
		entries, err := os.ReadDir(abs)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return out, fmt.Errorf("read %s: %w", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			ext := strings.ToLower(filepath.Ext(e.Name()))
			if ext == ".js" || ext == ".ts" {
				out.AutoDiscoveredPlugins = append(out.AutoDiscoveredPlugins, filepath.Join(dir, e.Name()))
			}
		}
	}
	// Pi's project-local surface executes at launch: .pi/extensions/ is
	// auto-discovered, and .pi/settings.json can declare extensions and
	// packages on top of it. Nothing declares either one to codebox, so
	// both are execution inputs (review finding on #345).
	piSettings := filepath.Join(".pi", "settings.json")
	if abs, err := confinedProjectPath(root, piSettings); err == nil {
		info, err := os.Lstat(abs)
		if err != nil {
			return out, fmt.Errorf("inspect %s: %w", piSettings, err)
		}
		if info.Mode().IsRegular() {
			out.PiProjectConfig = abs
		} else {
			return out, fmt.Errorf("%s is not a regular file", abs)
		}
	} else if !os.IsNotExist(err) {
		return out, fmt.Errorf("inspect %s: %w", piSettings, err)
	}
	if out.PiProjectConfig != "" {
		files, err := declaredPiCodeFiles(root, out.PiProjectConfig)
		if err != nil {
			return out, fmt.Errorf("inspect %s: %w", piSettings, err)
		}
		out.PiDeclaredCodeFiles = append(out.PiDeclaredCodeFiles, files...)
	}
	piExtensions := filepath.Join(".pi", "extensions")
	if abs, err := confinedProjectPath(root, piExtensions); err == nil {
		files, err := projectRegularFiles(root, abs)
		if err != nil {
			return out, fmt.Errorf("read %s: %w", piExtensions, err)
		}
		out.PiAutoDiscoveredExtensions = append(out.PiAutoDiscoveredExtensions, files...)
	} else if !os.IsNotExist(err) {
		return out, fmt.Errorf("read %s: %w", piExtensions, err)
	}
	sort.Strings(out.Plugins)
	sort.Strings(out.DeclaredPluginFiles)
	sort.Strings(out.AutoDiscoveredPlugins)
	sort.Strings(out.PiAutoDiscoveredExtensions)
	sort.Strings(out.PiDeclaredCodeFiles)
	sort.Strings(out.MCPCommands)
	sort.Strings(out.MCPEndpoints)
	return out, nil
}

// projectRegularFiles lists every regular file under dir, as paths relative to
// root. Symlinks and non-regular files are refused rather than skipped: an
// executable surface must not hide content outside the approval boundary, and
// WalkDir does not follow directory symlinks, so silently skipping one would
// leave exactly that content unapproved.
func projectRegularFiles(root, dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if _, err := confinedProjectPath(root, rel); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", path)
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

// declaredPluginFiles resolves explicit project-local plugin references without
// following symlinks. Relative references use the declaring config file's
// directory, matching OpenCode. Package names are not filesystem paths and
// remain covered by the declaring config's hash. A missing local target is left
// for OpenCode to diagnose, preserving the existing config-only behavior.
func declaredPluginFiles(root, configPath, ref string) ([]string, error) {
	target, local, err := declaredPluginTarget(configPath, ref)
	if err != nil || !local {
		return nil, err
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return nil, err
	}
	abs, err := confinedProjectPath(root, rel)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(abs)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode().IsRegular() {
		return []string{rel}, nil
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a regular file or directory", abs)
	}
	return projectRegularFiles(root, abs)
}

func declaredPluginTarget(configPath, ref string) (target string, local bool, err error) {
	if strings.HasPrefix(ref, "file://") {
		parsed, err := url.Parse(ref)
		if err != nil {
			return "", true, fmt.Errorf("parse file URL: %w", err)
		}
		if parsed.Host != "" && parsed.Host != "localhost" {
			return "", true, fmt.Errorf("file URL host %q is outside the local project", parsed.Host)
		}
		if parsed.RawQuery != "" || parsed.Fragment != "" {
			return "", true, fmt.Errorf("file URL must not contain a query or fragment")
		}
		path := parsed.Path
		if runtime.GOOS == "windows" && len(path) >= 3 && path[0] == '/' && path[2] == ':' {
			path = path[1:]
		}
		target = filepath.FromSlash(path)
		if !filepath.IsAbs(target) {
			return "", true, fmt.Errorf("file URL %q does not resolve to an absolute path", ref)
		}
		return target, true, nil
	}
	if strings.HasPrefix(ref, "./") || strings.HasPrefix(ref, "../") {
		return filepath.Join(filepath.Dir(configPath), ref), true, nil
	}
	if filepath.IsAbs(ref) {
		return filepath.Clean(ref), true, nil
	}
	return "", false, nil
}

// piExtensionMetacharacters are the characters that turn a declared path (an
// "extensions" entry, or a local "packages" source) into a glob pattern rather
// than a plain path.
const piExtensionMetacharacters = "*?[]{}"

// declaredPiCodeFiles resolves the local path declarations of a Pi settings file
// into the regular files Pi loads and runs at launch.
//
// Pi reads .pi/settings.json in preference to the managed global settings and
// resolves path-valued entries relative to .pi. Two arrays can name project
// files: "extensions" is the resource Pi executes directly, and "packages" may
// name a local file or directory, which Pi loads as an extension without
// copying it. Both have to be pinned by content here, or a change to a declared
// file would slip past the approval that the settings hash gates (#345 review).
// "skills", "prompts" and "themes" name content the model reads, and a package
// that comes from npm or git is a dependency whose contents are not in the
// project — the same boundary this package already draws for OpenCode, where a
// plugin declaration is hashed but the contents of an npm package are not.
//
// Resolution is deliberately conservative in one direction only: a glob pattern
// is resolved to its longest literal parent directory and everything under it
// is listed, so a matcher whose exact semantics codebox does not implement can
// only ever add files to the approval set, never hide one behind it. A pattern
// that resolves to the project root, a path above the project root, and a
// home-relative path are refused rather than skipped, because skipping one would
// reopen the bypass this resolution exists to close.
func declaredPiCodeFiles(root, settingsPath string) ([]string, error) {
	rel, err := filepath.Rel(root, settingsPath)
	if err != nil {
		return nil, fmt.Errorf("read project Pi settings %s: %w", settingsPath, err)
	}
	abs, err := confinedProjectPath(root, rel)
	if err != nil {
		return nil, fmt.Errorf("read project Pi settings %s: %w", settingsPath, err)
	}
	raw, err := readProjectConfigFile(abs)
	if err != nil {
		return nil, fmt.Errorf("read project Pi settings %s: %w", settingsPath, err)
	}
	// Pi's settings are plain JSON; the tolerant project-config decoder is
	// reused for them. Accepting more than Pi does can only add files to the
	// approval set, never drop one.
	parsed, err := parseProjectOpenCodeConfig(raw)
	if err != nil {
		return nil, fmt.Errorf("parse project Pi settings %s: %w", settingsPath, err)
	}
	// The declared kind only shapes the wording of an error, but it has to be
	// the kind the user wrote: "declared Pi extension" sent a user looking at
	// the wrong array of the wrong file.
	groups := []struct {
		kind    string
		values  []string
		remotes bool
	}{
		{kind: "extension", values: piExtensionEntries(parsed["extensions"])},
		{kind: "package", values: piPackageSources(parsed["packages"]), remotes: true},
	}
	var files []string
	for _, group := range groups {
		for _, entry := range group.values {
			declared := strings.TrimSpace(entry)
			if declared == "" {
				continue
			}
			// Exclusions subtract from what Pi loads, so they cannot add an input:
			// listing an excluded file would only be a false positive, and skipping
			// one can never drop a file that loads.
			if strings.HasPrefix(declared, "!") || strings.HasPrefix(declared, "-") {
				continue
			}
			declared = strings.TrimPrefix(declared, "+")
			if group.remotes && piPackageSourceIsRemote(declared) {
				continue
			}
			resolved, err := piDeclaredPathTargetFiles(root, abs, group.kind, declared)
			if err != nil {
				return nil, err
			}
			files = append(files, resolved...)
		}
	}
	sort.Strings(files)
	return files, nil
}

// piExtensionEntries normalizes the "extensions" value into the path strings it
// declares. Pi's schema types it as string[], so a bare string is accepted here
// too (a superset can only add inputs); any other shape is not a path Pi can
// load and contributes nothing.
func piExtensionEntries(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []any:
		var out []string
		for _, entry := range typed {
			if s, ok := entry.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// piPackageSources normalizes the "packages" value into the source strings it
// declares: Pi accepts a bare string or the object form, whose "source" field
// holds the same string. A filter in the object form narrows what a package
// loads, so it cannot add an input and is not resolved here.
func piPackageSources(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []any:
		var out []string
		for _, entry := range typed {
			switch item := entry.(type) {
			case string:
				out = append(out, item)
			case map[string]any:
				if source, ok := item["source"].(string); ok {
					out = append(out, source)
				}
			}
		}
		return out
	default:
		return nil
	}
}

// piPackageSourceIsRemote reports whether a package source names something Pi
// fetches rather than a project file. A dependency's contents are not in the
// project, so they cannot be pinned by content — the boundary this package
// already draws for OpenCode plugins. Anything left over is treated as a local
// path by the caller, which refuses it if it escapes the project, so an
// unrecognized protocol fails closed rather than silently skipping an input.
func piPackageSourceIsRemote(source string) bool {
	trimmed := strings.TrimSpace(source)
	if trimmed == "" {
		return true
	}
	for _, prefix := range []string{"npm:", "git:", "git@", "http://", "https://", "ssh://", "git://"} {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

// piDeclaredPathTargetFiles resolves one declared path of a Pi settings file into
// the project files it can make Pi load. Plain paths name a file or a directory;
// a glob pattern is widened to its longest literal parent directory, which may
// list files the pattern itself does not match. kind names the array the path
// came from and appears in the refusals, so they point at what the user wrote.
func piDeclaredPathTargetFiles(root, settingsPath, kind, entry string) ([]string, error) {
	declared := fmt.Sprintf("declared Pi %s %q", kind, entry)
	if entry == "~" || strings.HasPrefix(entry, "~/") || strings.HasPrefix(entry, `~\`) {
		return nil, fmt.Errorf("%s: a home-relative path cannot be pinned by content; move the file into the project", declared)
	}
	parent, isPattern := piExtensionPatternParent(entry)
	target := parent
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(settingsPath), target)
	}
	rel, err := filepath.Rel(root, filepath.Clean(target))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", declared, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("%s: resolves outside the project; only paths inside the project can be pinned by content", declared)
	}
	if isPattern && rel == "." {
		return nil, fmt.Errorf("%s: a pattern rooted at the project root matches too much to pin by content; list the files explicitly", declared)
	}
	abs, err := confinedProjectPath(root, rel)
	if os.IsNotExist(err) {
		// A declaration that resolves to nothing loads nothing.
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", declared, err)
	}
	info, err := os.Lstat(abs)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", declared, err)
	}
	if info.Mode().IsRegular() {
		return []string{rel}, nil
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s: %s is not a regular file or directory", declared, abs)
	}
	files, err := projectRegularFiles(root, abs)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", declared, err)
	}
	return files, nil
}

// piExtensionPatternParent splits an entry at its longest literal parent
// directory: "hooks/*.ts" yields "hooks", "../shared/**/*.ts" yields
// "../shared", and a pattern that starts wild ("*.ts") yields ".". Entries
// without metacharacters are returned unchanged with false.
func piExtensionPatternParent(entry string) (string, bool) {
	if !strings.ContainsAny(entry, piExtensionMetacharacters) {
		return entry, false
	}
	var literal []string
	for _, part := range strings.Split(filepath.ToSlash(entry), "/") {
		if strings.ContainsAny(part, piExtensionMetacharacters) {
			break
		}
		literal = append(literal, part)
	}
	if len(literal) == 0 {
		return ".", true
	}
	return filepath.FromSlash(strings.Join(literal, "/")), true
}
