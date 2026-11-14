// Command configcompose writes the managed OpenCode configurations exercised
// by the pinned host integration suite. It contains no assertions: the real
// OpenCode binary is the schema validator under test.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/etalab-ia/codebox/apps/codebox/assets"
	"github.com/etalab-ia/codebox/apps/codebox/internal/codebox"
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
	writeCompose(dir, "01-default", codebox.ManagedOverlay{})

	choices := codebox.CuratedAlbertModels()
	for i, choice := range choices {
		writeCompose(dir, fmt.Sprintf("02-albert-%d-%s", i, choice.OpenCodeID), codebox.ManagedOverlay{
			Model:      choice.Ref(),
			SmallModel: choice.Ref(),
			Models:     []codebox.AlbertModelConfig{choice.AlbertModelConfig()},
		})
	}

	allModels := make([]codebox.AlbertModelConfig, 0, len(choices))
	for _, choice := range choices {
		allModels = append(allModels, choice.AlbertModelConfig())
	}
	writeCompose(dir, "03-albert-main-and-small", codebox.ManagedOverlay{
		Model:      choices[1].Ref(),
		SmallModel: codebox.MinistralSmallModelRef,
		Models:     allModels,
	})

	writeCompose(dir, "05-managed-mcp", codebox.ManagedOverlay{MCPConnectors: []string{"playwright", "context7"}})

	writeCompose(dir, "04-provider-partial-metadata", codebox.ManagedOverlay{
		Model:      "openai-compatible/complete",
		SmallModel: "openai-compatible/context-only",
		Providers: []codebox.ProviderOverlay{{
			Kind: "openai-compatible", Name: "Metadata test", BaseURL: "https://api.example.com/v1", APIKeyEnv: codebox.OpenAICompatibleAPIKeyEnv,
			Models: []codebox.ProviderModelConfig{
				{ID: "complete", Name: "Complete", Context: 1024, Output: 256},
				{ID: "context-only", Name: "Context only", Context: 1024},
				{ID: "output-only", Name: "Output only", Output: 256},
				{ID: "unknown", Name: "Unknown"},
			},
		}},
	})
}

func writeCompose(dir, name string, overlay codebox.ManagedOverlay) {
	content, err := codebox.ComposeConfigContent(overlay)
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
