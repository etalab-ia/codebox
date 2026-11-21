package codebox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	msb "github.com/superradcompany/microsandbox/sdk/go"
)

const acpCleanupTimeout = 5 * time.Second

// msbACPStream is the bidirectional, non-TTY process channel required by ACP.
// It remains separate from msbClient so existing runtime fakes do not silently
// acquire a new host dependency.
type msbACPStream interface {
	TakeStdin() io.WriteCloser
	Recv(ctx context.Context) (*msb.ExecEvent, error)
	Kill(ctx context.Context) error
	Close() error
}

type msbACPStreamClient interface {
	ExecStream(ctx context.Context, name, command string, args []string, cwd string) (msbACPStream, error)
}

// RunACP connects an ACP client to OpenCode inside the prepared guest. It takes
// ownership of stdin and closes/joins it during cleanup. Output writers remain
// caller-owned: after cancellation the caller must unblock or close them to
// release any write still in progress. No CLI exposes this raw transport before
// the policy layer mediates client capabilities and session paths.
func (m *MicrosandboxRuntime) RunACP(ctx context.Context, stdin io.ReadCloser, stdout, stderr io.Writer) error {
	if m.cfg.EffectiveHarness() != HarnessOpenCode {
		return fmt.Errorf("ACP nécessite OpenCode ; le harnais %s n'a pas de point d'entrée ACP pris en charge", m.cfg.EffectiveHarness())
	}
	if m.cfg.Isolation != IsolationFull {
		return fmt.Errorf("ACP nécessite l'isolation full")
	}
	client, ok := m.Client.(msbACPStreamClient)
	if !ok {
		return fmt.Errorf("le client Microsandbox ne fournit pas l'exécution ACP en flux")
	}
	stream, err := client.ExecStream(ctx, m.InstanceName(), "opencode", []string{"acp"}, "/workspace")
	if err != nil {
		return fmt.Errorf("démarrage d'opencode acp dans %s : %w", m.InstanceName(), err)
	}
	return relayACP(ctx, stream, stdin, stdout, stderr)
}

type acpRecvResult struct {
	event *msb.ExecEvent
	err   error
}

// relayACP preserves the protocol bytes exactly. Recv, input copying and output
// writes each have at most one active operation, so no two writes can reorder
// protocol chunks. Client-input closure is joined before the relay returns.
func relayACP(ctx context.Context, stream msbACPStream, stdin io.ReadCloser, stdout, stderr io.Writer) (retErr error) {
	defer func() { retErr = errors.Join(retErr, stream.Close()) }()

	sink := stream.TakeStdin()
	if sink == nil {
		return fmt.Errorf("le flux ACP invité n'a pas de stdin")
	}
	relayCtx, cancelRelay := context.WithCancelCause(ctx)
	defer cancelRelay(nil)
	defer func() { _ = sink.Close() }()

	inputDone := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(sink, stdin)
		err := errors.Join(copyErr, sink.Close())
		if err != nil {
			cancelRelay(err)
		}
		inputDone <- err
	}()
	defer func() {
		_ = stdin.Close()
		if inputDone != nil {
			<-inputDone
		}
	}()

	recvDone := make(chan acpRecvResult, 1)
	startRecv := func() {
		go func() {
			event, err := stream.Recv(relayCtx)
			recvDone <- acpRecvResult{event: event, err: err}
		}()
	}
	startRecv()

	exited := false
	exitCode := 0
	for {
		select {
		case <-ctx.Done():
			return terminateACP(stream, ctx.Err())
		case err := <-inputDone:
			inputDone = nil
			if err != nil {
				return terminateACP(stream, fmt.Errorf("transmission de l'entrée ACP : %w", err))
			}
		case result := <-recvDone:
			if result.err != nil {
				if cause := context.Cause(relayCtx); cause != nil {
					return terminateACP(stream, fmt.Errorf("annulation du flux ACP : %w", cause))
				}
				return terminateACP(stream, fmt.Errorf("réception du flux ACP invité : %w", result.err))
			}
			if result.event == nil {
				return terminateACP(stream, fmt.Errorf("réception d'un événement ACP invité vide"))
			}
			event := result.event
			switch event.Kind {
			case msb.ExecEventStarted:
				// The guest PID is runtime bookkeeping, not ACP protocol output.
			case msb.ExecEventStdout:
				if err := writeACPChunkContext(relayCtx, stdout, event.Data); err != nil {
					return terminateACP(stream, fmt.Errorf("écriture de la sortie ACP : %w", err))
				}
			case msb.ExecEventStderr:
				if err := writeACPChunkContext(relayCtx, stderr, event.Data); err != nil {
					return terminateACP(stream, fmt.Errorf("écriture des diagnostics ACP : %w", err))
				}
			case msb.ExecEventFailed:
				return fmt.Errorf("démarrage du processus ACP invité : %s", acpFailureMessage(event.Failure))
			case msb.ExecEventStdinError:
				return terminateACP(stream, fmt.Errorf("entrée du processus ACP invité : %s", acpFailureMessage(event.Failure)))
			case msb.ExecEventExited:
				exited = true
				exitCode = event.ExitCode
			case msb.ExecEventDone:
				if !exited {
					return fmt.Errorf("le flux ACP invité s'est terminé sans statut de sortie")
				}
				if exitCode != 0 {
					return fmt.Errorf("le processus ACP OpenCode s'est terminé avec le code %d", exitCode)
				}
				return nil
			default:
				return terminateACP(stream, fmt.Errorf("événement ACP invité inconnu : %v", event.Kind))
			}
			startRecv()
		}
	}
}

// writeACPChunkContext makes a blocked client writer unable to prevent guest
// cancellation. The writer remains caller-owned; its goroutine can finish when
// the caller closes or unblocks it after RunACP returns.
func writeACPChunkContext(ctx context.Context, writer io.Writer, data []byte) error {
	done := make(chan error, 1)
	go func() { done <- writeACPChunk(writer, data) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func writeACPChunk(writer io.Writer, data []byte) error {
	n, err := writer.Write(data)
	if err == nil && n != len(data) {
		return io.ErrShortWrite
	}
	return err
}

func acpFailureMessage(failure *msb.ExecFailure) string {
	if failure == nil || failure.Message == "" {
		return "erreur non détaillée"
	}
	return failure.Message
}

func terminateACP(stream msbACPStream, cause error) error {
	ctx, cancel := context.WithTimeout(context.Background(), acpCleanupTimeout)
	defer cancel()
	if err := stream.Kill(ctx); err != nil {
		return errors.Join(cause, fmt.Errorf("arrêt du processus ACP invité : %w", err))
	}
	return cause
}
