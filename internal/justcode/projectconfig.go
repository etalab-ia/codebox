package justcode

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// This file reads the OpenCode project configuration, read-only. Per D-001:
// JSONC is discovered at four locations, comments are legal, and the file is
// never rewritten. just-code parses it only to detect managed-field
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
// OpenCode inputs just-code must gate behind host-local trust approval
// before OpenCode loads them. Per D-001: declared AND auto-discovered
// plugins execute arbitrary code at config load; MCP server commands launch
// host processes from the project config.
type ProjectExecutionInputs struct {
	// ConfigPath is the project OpenCode config path, when one exists.
	ConfigPath string
	// Plugins lists project plugin declarations (the "plugin" field values
	// from the config).
	Plugins []string
	// AutoDiscoveredPlugins lists .js/.ts files under .opencode/plugin/
	// and .opencode/plugins/ — they execute without any declaration.
	AutoDiscoveredPlugins []string
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
	sort.Strings(out.Plugins)
	sort.Strings(out.AutoDiscoveredPlugins)
	sort.Strings(out.MCPCommands)
	sort.Strings(out.MCPEndpoints)
	return out, nil
}
