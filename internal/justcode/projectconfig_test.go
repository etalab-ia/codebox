package justcode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseProjectOpenCodeConfigJSONC(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".opencode", "opencode.jsonc")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	src := `{
  // model chosen by the team
  "model": "albert/deepseek-v4-flash",
  "mcp": {
    "docs": { "command": "npx", "args": ["-y", "docs-server"] } /* local */
  },
  "plugin": ["./tools/format.ts",],
}`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseProjectOpenCodeConfig(dir, path)
	if err != nil {
		t.Fatalf("JSONC must parse: %v", err)
	}
	if parsed["model"] != "albert/deepseek-v4-flash" {
		t.Fatalf("model = %v", parsed["model"])
	}
	// Comment markers inside strings survive stripping.
	mcp := parsed["mcp"].(map[string]any)["docs"].(map[string]any)
	if mcp["command"] != "npx" {
		t.Fatalf("mcp command = %v", mcp)
	}
}

func TestStripJSONCCommentsPreservesStringContents(t *testing.T) {
	src := `{"a": "http://x//y", "b": "/* not a comment */"} // trailing`
	got := stripJSONCComments(src)
	if !strings.Contains(got, `"http://x//y"`) {
		t.Fatalf("string slashes must survive: %q", got)
	}
	if !strings.Contains(got, `"/* not a comment */"`) {
		t.Fatalf("string comment markers must survive: %q", got)
	}
	if strings.Contains(got, "// trailing") {
		t.Fatalf("the trailing comment must be stripped: %q", got)
	}
}

func TestParseProjectOpenCodeConfigRejectsUnterminatedBlockComment(t *testing.T) {
	if _, err := parseProjectOpenCodeConfig([]byte(`{"model":"albert/model"} /* missing terminator`)); err == nil {
		t.Fatal("unterminated JSONC block comment was silently stripped")
	}
}

func TestParseProjectOpenCodeConfigRejectsMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")
	if err := os.WriteFile(path, []byte(`{"model": broken`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseProjectOpenCodeConfig(dir, path); err == nil {
		t.Fatal("a malformed project config must be an error, never 'no conflicts'")
	} else if !strings.Contains(err.Error(), path) {
		t.Fatalf("parse error omitted source path %q: %v", path, err)
	}
	// Empty path is a normal state.
	if m, err := ParseProjectOpenCodeConfig(dir, ""); err != nil || m == nil {
		t.Fatalf("empty path: %v, %v", m, err)
	}
}

func TestDiscoverProjectExecutionInputs(t *testing.T) {
	dir := t.TempDir()
	// A config with plugins and an MCP command.
	cfg := filepath.Join(dir, "opencode.json")
	if err := os.WriteFile(cfg, []byte(`{
		"plugin": ["./tools/p.ts"],
		"mcp": {"docs": {"command": "npx"}, "remote": {"url": "https://example.com/sse"}}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Auto-discovered plugins (.js and .ts execute; .mjs does not, per D-001).
	for _, rel := range []string{".opencode/plugin/a.js", ".opencode/plugins/b.ts", ".opencode/plugin/c.mjs"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, rel), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	inputs, err := DiscoverProjectExecutionInputs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if inputs.ConfigPath != cfg {
		t.Fatalf("config path = %s", inputs.ConfigPath)
	}
	if len(inputs.Plugins) != 1 || inputs.Plugins[0] != "./tools/p.ts" {
		t.Fatalf("plugins = %v", inputs.Plugins)
	}
	if len(inputs.MCPCommands) != 1 || inputs.MCPCommands[0] != "npx" {
		t.Fatalf("mcp commands = %v (remote URLs are not commands)", inputs.MCPCommands)
	}
	if len(inputs.MCPEndpoints) != 1 || inputs.MCPEndpoints[0] != "https://example.com/sse" {
		t.Fatalf("mcp endpoints = %v", inputs.MCPEndpoints)
	}
	if len(inputs.AutoDiscoveredPlugins) != 2 {
		t.Fatalf("auto-discovered = %v (mjs must be excluded)", inputs.AutoDiscoveredPlugins)
	}
}

func TestConfinedProjectPathRejectsTraversalAndAbsolute(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"../escape.js", "sub/../../escape.js", string(filepath.Separator) + "abs.js"} {
		if _, err := confinedProjectPath(root, rel); err == nil {
			t.Fatalf("project path %q must be refused", rel)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, ".opencode", "plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".opencode", "plugin", "a.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, ".opencode", "plugin", "a.js")
	if got, err := confinedProjectPath(root, filepath.Join(".opencode", "plugin", "a.js")); err != nil || got != want {
		t.Fatalf("clean relative path: got %s, %v; want %s", got, err, want)
	}
}

func TestDiscoverProjectExecutionInputsEmptyRoot(t *testing.T) {
	inputs, err := DiscoverProjectExecutionInputs(t.TempDir())
	if err != nil {
		t.Fatalf("a checkout without .opencode or .pi must discover cleanly: %v", err)
	}
	if inputs.ConfigPath != "" || len(inputs.AutoDiscoveredPlugins) != 0 ||
		inputs.PiProjectConfig != "" || len(inputs.PiAutoDiscoveredExtensions) != 0 {
		t.Fatalf("unexpected inputs: %+v", inputs)
	}
}

func TestParseProjectOpenCodeConfigRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "real.json")
	if err := os.WriteFile(target, []byte(`{"plugin": ["./p.ts"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "opencode.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	// Discovery keeps reporting the entry so the trust flow can block on it;
	// only the read refuses.
	if got := FindProjectOpenCodeConfig(dir); got != link {
		t.Fatalf("discovery must keep reporting the symlinked config, got %q", got)
	}
	if _, err := ParseProjectOpenCodeConfig(dir, link); err == nil {
		t.Fatal("parse must refuse a symlinked config instead of reading its target")
	}
	if _, err := DiscoverProjectExecutionInputs(dir); err == nil {
		t.Fatal("input discovery must fail closed on a symlinked config")
	}
}

func TestParseProjectOpenCodeConfigSizeLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")
	oversized := `{"x": "` + strings.Repeat("a", maxOpenCodeConfigSize) + `"}`
	if err := os.WriteFile(path, []byte(oversized), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseProjectOpenCodeConfig(dir, path); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("expected size-limit refusal, got %v", err)
	}
	atLimit := `{"x": "` + strings.Repeat("a", maxOpenCodeConfigSize-len(`{"x": ""}`)) + `"}`
	if err := os.WriteFile(path, []byte(atLimit), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseProjectOpenCodeConfig(dir, path); err != nil {
		t.Fatalf("a config at exactly the limit must parse: %v", err)
	}
}

// TestDiscoverProjectExecutionInputsResolvesDeclaredPiExtensions covers the
// local path forms Pi accepts in a settings file's "extensions" array. A glob is
// widened to its longest literal parent directory, so a matcher whose exact
// semantics just-code does not implement can only add files to the approval set.
func TestDiscoverProjectExecutionInputsResolvesDeclaredPiExtensions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings func(root string) string
		files    []string
		want     []string
	}{
		{
			name:     "file declaration resolved relative to .pi",
			settings: func(string) string { return `{"extensions":["../hooks/startup.ts"]}` },
			files:    []string{"hooks/startup.ts"},
			want:     []string{filepath.Join("hooks", "startup.ts")},
		},
		{
			name:     "force-include marker",
			settings: func(string) string { return `{"extensions":["+../hooks/startup.ts"]}` },
			files:    []string{"hooks/startup.ts"},
			want:     []string{filepath.Join("hooks", "startup.ts")},
		},
		{
			name: "absolute path inside the project",
			settings: func(root string) string {
				return `{"extensions":["` + filepath.ToSlash(filepath.Join(root, "hooks", "startup.ts")) + `"]}`
			},
			files: []string{"hooks/startup.ts"},
			want:  []string{filepath.Join("hooks", "startup.ts")},
		},
		{
			name:     "directory declaration lists every file under it",
			settings: func(string) string { return `{"extensions":["extras"]}` },
			files:    []string{".pi/extras/one.ts", ".pi/extras/nested/two.ts"},
			want: []string{
				filepath.Join(".pi", "extras", "nested", "two.ts"),
				filepath.Join(".pi", "extras", "one.ts"),
			},
		},
		{
			name:     "a glob is widened to its literal parent directory",
			settings: func(string) string { return `{"extensions":["../hooks/*.ts"]}` },
			files:    []string{"hooks/startup.ts", "hooks/readme.md"},
			want: []string{
				filepath.Join("hooks", "readme.md"),
				filepath.Join("hooks", "startup.ts"),
			},
		},
		{
			name:     "exclusions never add an input",
			settings: func(string) string { return `{"extensions":["!../hooks/startup.ts","-../hooks/other.ts"]}` },
			files:    []string{"hooks/startup.ts", "hooks/other.ts"},
			want:     nil,
		},
		{
			name:     "a missing declaration loads nothing",
			settings: func(string) string { return `{"extensions":["../hooks/gone.ts"]}` },
			want:     nil,
		},
		{
			name:     "a pattern whose parent is missing loads nothing",
			settings: func(string) string { return `{"extensions":["nothing/*.ts"]}` },
			want:     nil,
		},
		{
			name:     "a bare string is read as a single declaration",
			settings: func(string) string { return `{"extensions":"../hooks/startup.ts"}` },
			files:    []string{"hooks/startup.ts"},
			want:     []string{filepath.Join("hooks", "startup.ts")},
		},
		{
			name:     "no extensions array",
			settings: func(string) string { return `{"packages":["npm:some-tool"]}` },
			want:     nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, ".pi"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, ".pi", "settings.json"), []byte(tc.settings(root)), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, rel := range tc.files {
				path := filepath.Join(root, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("// v1"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			inputs, err := DiscoverProjectExecutionInputs(root)
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Join(inputs.PiDeclaredExtensionFiles, ",")
			want := strings.Join(tc.want, ",")
			if got != want {
				t.Fatalf("declared Pi extensions = %q; want %q", got, want)
			}
		})
	}
}

// TestDeclaredPiExtensionsRefuseUnpinnablePaths keeps the resolution fail-closed:
// a declaration that cannot be tied to project content must refuse the launch
// rather than be skipped, or the settings hash would approve a declaration whose
// target stays unreviewed.
func TestDeclaredPiExtensionsRefuseUnpinnablePaths(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "evil.ts")
	outside := t.TempDir()
	symlinkSettings := func(root string) string {
		return `{"extensions":["../link/evil.ts"]}`
	}
	for _, tc := range []struct {
		name     string
		settings func(root string) string
		prepare  func(t *testing.T, root string) bool
	}{
		{
			name:     "home-relative path",
			settings: func(string) string { return `{"extensions":["~/.pi/agent/extensions/evil.ts"]}` },
		},
		{
			name:     "absolute path outside the project",
			settings: func(string) string { return `{"extensions":["` + filepath.ToSlash(abs) + `"]}` },
		},
		{
			name:     "path above the project root",
			settings: func(string) string { return `{"extensions":["../../hooks/evil.ts"]}` },
		},
		{
			name:     "pattern rooted at the project root",
			settings: func(string) string { return `{"extensions":["../**/*.ts"]}` },
		},
		{
			name:     "pattern starting wild at the project root",
			settings: func(string) string { return `{"extensions":["../../*/*.ts"]}` },
		},
		{
			name:     "malformed settings",
			settings: func(string) string { return `{"extensions":` },
		},
		{
			name:     "declared path behind a symlink",
			settings: symlinkSettings,
			prepare: func(t *testing.T, root string) bool {
				if err := os.WriteFile(filepath.Join(outside, "evil.ts"), []byte("// outside"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				return true
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, ".pi"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, ".pi", "settings.json"), []byte(tc.settings(root)), 0o644); err != nil {
				t.Fatal(err)
			}
			if tc.prepare != nil && !tc.prepare(t, root) {
				return
			}
			if _, err := DiscoverProjectExecutionInputs(root); err == nil {
				t.Fatal("this declaration cannot be pinned by content and must refuse the launch")
			}
		})
	}
}
