package codebox

import (
	"context"
	"fmt"
)

// legacyDockerContainer is the container created by the Docker runtime that
// this version no longer ships. It is kept here for migration only: an upgrade
// can leave it running, and because the Compose file declared it with
// `restart: unless-stopped` and published ports 4096 and 3000-3010 - the same
// ports the supported runtimes use - it would otherwise keep those ports bound
// while the CLI could neither detect nor stop the cause.
const legacyDockerContainer = "albert-opencode-sandbox"

// legacyDockerContainerExists reports whether the removed Docker runtime's
// container is still present on this host. A host without the `docker` CLI is
// not an error: the runtime is gone and most hosts will not have it any more.
func legacyDockerContainerExists(ctx context.Context, r Runner) bool {
	res, err := r.Run(ctx, "docker", "inspect", "--format", "{{.State.Status}}", legacyDockerContainer)
	if err != nil {
		return false
	}
	return res.ExitCode == 0
}

// removeLegacyDockerContainer force-removes the container left behind by the
// removed Docker runtime. A host without Docker, or without the container, is
// left alone.
func removeLegacyDockerContainer(ctx context.Context, r Runner) error {
	res, err := r.Run(ctx, "docker", "rm", "--force", legacyDockerContainer)
	if err != nil {
		return nil
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("docker rm --force %s failed (exit %d)", legacyDockerContainer, res.ExitCode)
	}
	fmt.Printf("Removed the legacy Docker container %q left by an earlier codebox version.\n", legacyDockerContainer)
	return nil
}

// clearLegacyDocker migrates a host that still carries the removed Docker
// runtime's container. It runs before any runtime is started so the stale
// container cannot hold the backend ports, and on `stop` so the container is
// not left behind forever.
func (d *Dispatcher) clearLegacyDocker(ctx context.Context) error {
	r := d.Runner
	if r == nil {
		r = OSRunner{}
	}
	if !legacyDockerContainerExists(ctx, r) {
		return nil
	}
	return removeLegacyDockerContainer(ctx, r)
}
