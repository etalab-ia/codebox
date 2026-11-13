package codebox

import (
	"context"
	"io"
	"os/exec"
	"testing"
)

// missingDockerRunner simulates a host where the `docker` CLI is not installed,
// which is the normal case once the Docker runtime is gone.
type missingDockerRunner struct{}

func (missingDockerRunner) Run(context.Context, string, ...string) (ExecResult, error) {
	return ExecResult{}, exec.ErrNotFound
}

func (missingDockerRunner) RunEnv(context.Context, []string, string, ...string) (ExecResult, error) {
	return ExecResult{}, exec.ErrNotFound
}

func (missingDockerRunner) RunStdin(context.Context, io.Reader, string, ...string) (ExecResult, error) {
	return ExecResult{}, exec.ErrNotFound
}

// legacyDockerRunner reports the legacy container as present, a successful
// removal, and every other command as failing.
func legacyDockerRunner() *fakeRunner {
	return &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if name == "docker" && len(args) > 0 {
			switch args[0] {
			case "inspect":
				return ExecResult{ExitCode: 0, Stdout: "running"}
			case "rm":
				return ExecResult{ExitCode: 0}
			}
		}
		return ExecResult{ExitCode: 1}
	}}
}

func TestLegacyDockerContainerExists(t *testing.T) {
	present := legacyDockerRunner()
	if !legacyDockerContainerExists(context.Background(), present) {
		t.Error("expected the legacy container to be detected")
	}
	if !present.hasCall("docker inspect --format {{.State.Status}} " + legacyDockerContainer) {
		t.Errorf("unexpected probe: %v", present.calls)
	}

	absent := &fakeRunner{onRun: func(string, []string) ExecResult {
		return ExecResult{ExitCode: 1}
	}}
	if legacyDockerContainerExists(context.Background(), absent) {
		t.Error("expected no legacy container")
	}

	if legacyDockerContainerExists(context.Background(), missingDockerRunner{}) {
		t.Error("a host without docker must not report a legacy container")
	}
}

func TestRemoveLegacyDockerContainer(t *testing.T) {
	r := &fakeRunner{}
	if err := removeLegacyDockerContainer(context.Background(), r); err != nil {
		t.Fatalf("removeLegacyDockerContainer: %v", err)
	}
	if !r.hasCall("docker rm --force " + legacyDockerContainer) {
		t.Fatalf("expected a docker rm call, got %v", r.calls)
	}
}

func TestRemoveLegacyDockerContainerReportsFailure(t *testing.T) {
	r := &fakeRunner{onRun: func(string, []string) ExecResult {
		return ExecResult{ExitCode: 1}
	}}
	if err := removeLegacyDockerContainer(context.Background(), r); err == nil {
		t.Fatal("expected an error when docker rm fails")
	}
}

func TestRemoveLegacyDockerContainerSkipsMissingDocker(t *testing.T) {
	if err := removeLegacyDockerContainer(context.Background(), missingDockerRunner{}); err != nil {
		t.Fatalf("a host without docker must be left alone: %v", err)
	}
}

func legacyDispatcher(r Runner) *Dispatcher {
	d := NewDispatcherWith(Config{}, map[Runtime]Backend{
		RuntimeMicrosandbox: &fakeBackend{id: RuntimeMicrosandbox},
		RuntimeTart:         &fakeBackend{id: RuntimeTart},
		RuntimeAgentVM:      &fakeBackend{id: RuntimeAgentVM},
	})
	d.Runner = r
	return d
}

func TestStopAllRemovesLegacyDockerContainer(t *testing.T) {
	r := legacyDockerRunner()
	if err := legacyDispatcher(r).StopAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !r.hasCall("docker rm --force " + legacyDockerContainer) {
		t.Fatalf("expected the legacy container to be removed, got %v", r.calls)
	}
}

func TestPrepareRemovesLegacyDockerContainer(t *testing.T) {
	r := legacyDockerRunner()
	if err := legacyDispatcher(r).Prepare(context.Background(), RuntimeMicrosandbox); err != nil {
		t.Fatal(err)
	}
	if !r.hasCall("docker rm --force " + legacyDockerContainer) {
		t.Fatalf("expected the legacy container to be removed, got %v", r.calls)
	}
}

func TestLegacyDockerCleanupLeavesAbsentContainerAlone(t *testing.T) {
	r := &fakeRunner{onRun: func(string, []string) ExecResult {
		return ExecResult{ExitCode: 1}
	}}
	if err := legacyDispatcher(r).StopAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.hasCall("docker rm") {
		t.Fatalf("must not remove a container that is not there: %v", r.calls)
	}
}

func TestLegacyDockerCleanupSkipsHostWithoutDocker(t *testing.T) {
	if err := legacyDispatcher(missingDockerRunner{}).StopAll(context.Background()); err != nil {
		t.Fatalf("a host without docker must be left alone: %v", err)
	}
}
