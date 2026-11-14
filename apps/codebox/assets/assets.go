// Package assets holds guest resources embedded into the codebox binary.
package assets

import "embed"

//go:embed opencode-config.json guest-prep.sh pi-guest-prep.sh browser-guest-prep.sh agentvm-base-prep.sh
var FS embed.FS

// Files lists every embedded asset, in a stable order.
var Files = []string{
	"opencode-config.json",
	"guest-prep.sh",
	"pi-guest-prep.sh",
	"browser-guest-prep.sh",
	"agentvm-base-prep.sh",
}

// Read returns an embedded asset by name.
func Read(name string) ([]byte, error) {
	return FS.ReadFile(name)
}
