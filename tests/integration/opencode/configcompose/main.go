// Command configcompose writes the managed OpenCode configurations exercised
// by the pinned host integration suite. It contains no assertions: the real
// OpenCode binary is the schema validator under test.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/etalab-ia/just-code/assets"
	"github.com/etalab-ia/just-code/internal/justcode"
)

func main() {
	if len(os.Args) != 2 {
		fatalf("usage: go run ./tests/integration/opencode/configcompose <output-dir>")
	}
	dir := os.Args[1]
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fatalf("create output directory: %v", err)
	}

	embedded, err := assets.Read("opencode-config.json")
	if err != nil {
		fatalf("read embedded config: %v", err)
	}
	write(filepath.Join(dir, "00-embedded.json"), string(embedded))
	writeCompose(dir, "01-default", justcode.ManagedOverlay{})

	choices := justcode.CuratedAlbertModels()
	for i, choice := range choices {
		writeCompose(dir, fmt.Sprintf("02-albert-%d-%s", i, choice.OpenCodeID), justcode.ManagedOverlay{
			Model:      choice.Ref(),
			SmallModel: choice.Ref(),
			Models:     []justcode.AlbertModelConfig{choice.AlbertModelConfig()},
		})
	}

	allModels := make([]justcode.AlbertModelConfig, 0, len(choices))
	for _, choice := range choices {
		allModels = append(allModels, choice.AlbertModelConfig())
	}
	writeCompose(dir, "03-albert-main-and-small", justcode.ManagedOverlay{
		Model:      choices[1].Ref(),
		SmallModel: justcode.MinistralSmallModelRef,
		Models:     allModels,
	})

	writeCompose(dir, "05-managed-mcp", justcode.ManagedOverlay{MCPConnectors: []string{"playwright", "context7"}})

	writeCompose(dir, "04-provider-partial-metadata", justcode.ManagedOverlay{
		Model:      "openai-compatible/complete",
		SmallModel: "openai-compatible/context-only",
		Providers: []justcode.ProviderOverlay{{
			Kind: "openai-compatible", Name: "Metadata test", BaseURL: "https://api.example.com/v1", APIKeyEnv: justcode.OpenAICompatibleAPIKeyEnv,
			Models: []justcode.ProviderModelConfig{
				{ID: "complete", Name: "Complete", Context: 1024, Output: 256},
				{ID: "context-only", Name: "Context only", Context: 1024},
				{ID: "output-only", Name: "Output only", Output: 256},
				{ID: "unknown", Name: "Unknown"},
			},
		}},
	})
}

func writeCompose(dir, name string, overlay justcode.ManagedOverlay) {
	content, err := justcode.ComposeConfigContent(overlay)
	if err != nil {
		fatalf("compose %s: %v", name, err)
	}
	write(filepath.Join(dir, name+".json"), content)
}

func write(path, content string) {
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		fatalf("write %s: %v", path, err)
	}
}

func fatalf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
