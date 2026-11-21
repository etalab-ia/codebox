package codebox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	msb "github.com/superradcompany/microsandbox/sdk/go"
)

type fakeACPExecResult struct {
	event *msb.ExecEvent
	err   error
}

type fakeACPSink struct {
	mu        sync.Mutex
	input     bytes.Buffer
	closed    bool
	closedCh  chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func (s *fakeACPSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, errors.New("write on closed ACP stdin")
	}
	return s.input.Write(p)
}

func (s *fakeACPSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.closeOnce.Do(func() { close(s.closedCh) })
	return s.closeErr
}

func (s *fakeACPSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.input.String()
}

func (s *fakeACPSink) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

type fakeACPStream struct {
	mu          sync.Mutex
	results     []fakeACPExecResult
	sink        *fakeACPSink
	killCalls   int
	killErr     error
	closeCalls  int
	closeErr    error
	recvStarted chan struct{}
	recvOnce    sync.Once
}

func newFakeACPStream(results ...fakeACPExecResult) *fakeACPStream {
	return &fakeACPStream{
		results:     results,
		sink:        &fakeACPSink{closedCh: make(chan struct{})},
		recvStarted: make(chan struct{}),
	}
}

func (s *fakeACPStream) TakeStdin() io.WriteCloser { return s.sink }

func (s *fakeACPStream) Recv(ctx context.Context) (*msb.ExecEvent, error) {
	s.recvOnce.Do(func() { close(s.recvStarted) })
	s.mu.Lock()
	if len(s.results) > 0 {
		result := s.results[0]
		s.results = s.results[1:]
		s.mu.Unlock()
		if result.event != nil && result.event.Kind == msb.ExecEventExited {
			select {
			case <-s.sink.closedCh:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return result.event, result.err
	}
	s.mu.Unlock()
	<-ctx.Done()
	return nil, ctx.Err()
}

func (s *fakeACPStream) Kill(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.killCalls++
	return s.killErr
}

func (s *fakeACPStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeCalls++
	return s.closeErr
}

func (s *fakeACPStream) state() (kills, closes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.killCalls, s.closeCalls
}

type fakeACPStreamClient struct {
	*fakeMSBClient
	stream   *fakeACPStream
	startErr error
	calls    int
	name     string
	command  string
	args     []string
	cwd      string
}

func (c *fakeACPStreamClient) ExecStream(_ context.Context, name, command string, args []string, cwd string) (msbACPStream, error) {
	c.calls++
	c.name = name
	c.command = command
	c.args = append([]string(nil), args...)
	c.cwd = cwd
	if c.startErr != nil {
		return nil, c.startErr
	}
	return c.stream, nil
}

func acpEvents(results ...*msb.ExecEvent) []fakeACPExecResult {
	out := make([]fakeACPExecResult, 0, len(results))
	for _, event := range results {
		out = append(out, fakeACPExecResult{event: event})
	}
	return out
}

func TestRunACPStreamsProtocolBytes(t *testing.T) {
	stream := newFakeACPStream(acpEvents(
		&msb.ExecEvent{Kind: msb.ExecEventStarted, PID: 42},
		&msb.ExecEvent{Kind: msb.ExecEventStdout, Data: []byte("{\"jsonrpc\":")},
		&msb.ExecEvent{Kind: msb.ExecEventStderr, Data: []byte("guest diagnostic\n")},
		&msb.ExecEvent{Kind: msb.ExecEventStdout, Data: []byte("\"2.0\",\"id\":1,\"result\":{}}\n")},
		&msb.ExecEvent{Kind: msb.ExecEventExited, ExitCode: 0},
		&msb.ExecEvent{Kind: msb.ExecEventDone},
	)...)
	client := &fakeACPStreamClient{fakeMSBClient: &fakeMSBClient{}, stream: stream}
	runtime := NewMicrosandboxRuntimeForInstance(Config{
		Harness:   HarnessOpenCode,
		Isolation: IsolationFull,
	}, "project-acp")
	runtime.Client = client

	input := "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{}}\n"
	var stdout, stderr bytes.Buffer
	if err := runtime.RunACP(context.Background(), io.NopCloser(strings.NewReader(input)), &stdout, &stderr); err != nil {
		t.Fatalf("RunACP: %v", err)
	}

	if got := stream.sink.String(); got != input {
		t.Fatalf("guest stdin = %q, want exact client bytes %q", got, input)
	}
	wantStdout := "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n"
	if got := stdout.String(); got != wantStdout {
		t.Fatalf("host stdout = %q, want only ACP bytes %q", got, wantStdout)
	}
	if got := stderr.String(); got != "guest diagnostic\n" {
		t.Fatalf("host stderr = %q, want guest diagnostic", got)
	}
	if !stream.sink.isClosed() {
		t.Fatal("guest stdin was not closed after client EOF")
	}
	if kills, closes := stream.state(); kills != 0 || closes != 1 {
		t.Fatalf("stream kills/closes = %d/%d, want 0/1", kills, closes)
	}
	if client.calls != 1 || client.name != "project-acp" || client.command != "opencode" || strings.Join(client.args, " ") != "acp" || client.cwd != "/workspace" {
		t.Fatalf("ExecStream call = count %d, name %q, command %q, args %v, cwd %q", client.calls, client.name, client.command, client.args, client.cwd)
	}
}

func TestRunACPReportsGuestExitCode(t *testing.T) {
	stream := newFakeACPStream(acpEvents(
		&msb.ExecEvent{Kind: msb.ExecEventExited, ExitCode: 17},
		&msb.ExecEvent{Kind: msb.ExecEventDone},
	)...)
	runtime := NewMicrosandboxRuntime(Config{Harness: HarnessOpenCode, Isolation: IsolationFull})
	runtime.Client = &fakeACPStreamClient{fakeMSBClient: &fakeMSBClient{}, stream: stream}

	err := runtime.RunACP(context.Background(), io.NopCloser(strings.NewReader("")), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "17") {
		t.Fatalf("RunACP error = %v, want guest exit code 17", err)
	}
	if kills, closes := stream.state(); kills != 0 || closes != 1 {
		t.Fatalf("stream kills/closes = %d/%d, want 0/1 after observed exit", kills, closes)
	}
}

func TestRunACPCancellationKillsGuest(t *testing.T) {
	stream := newFakeACPStream()
	runtime := NewMicrosandboxRuntime(Config{Harness: HarnessOpenCode, Isolation: IsolationFull})
	runtime.Client = &fakeACPStreamClient{fakeMSBClient: &fakeMSBClient{}, stream: stream}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runtime.RunACP(ctx, io.NopCloser(strings.NewReader("")), &bytes.Buffer{}, &bytes.Buffer{})
	}()

	<-stream.recvStarted
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("RunACP error = %v, want context.Canceled", err)
	}
	if kills, closes := stream.state(); kills != 1 || closes != 1 {
		t.Fatalf("stream kills/closes = %d/%d, want 1/1 after cancellation", kills, closes)
	}
}

type blockingACPReader struct {
	closed chan struct{}
	once   sync.Once
}

func newBlockingACPReader() *blockingACPReader {
	return &blockingACPReader{closed: make(chan struct{})}
}

func (r *blockingACPReader) Read([]byte) (int, error) {
	<-r.closed
	return 0, io.EOF
}

func (r *blockingACPReader) Close() error {
	r.once.Do(func() { close(r.closed) })
	return nil
}

func TestRunACPCancellationClosesAndJoinsInput(t *testing.T) {
	stream := newFakeACPStream()
	runtime := NewMicrosandboxRuntime(Config{Harness: HarnessOpenCode, Isolation: IsolationFull})
	runtime.Client = &fakeACPStreamClient{fakeMSBClient: &fakeMSBClient{}, stream: stream}
	reader := newBlockingACPReader()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runtime.RunACP(ctx, reader, &bytes.Buffer{}, &bytes.Buffer{})
	}()

	<-stream.recvStarted
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunACP error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		_ = reader.Close()
		t.Fatal("RunACP did not return after cancellation")
	}
	select {
	case <-reader.closed:
	default:
		_ = reader.Close()
		t.Fatal("RunACP returned without closing and joining client input")
	}
}

type blockingACPWriter struct {
	started chan struct{}
	unblock chan struct{}
	once    sync.Once
}

func newBlockingACPWriter() *blockingACPWriter {
	return &blockingACPWriter{started: make(chan struct{}), unblock: make(chan struct{})}
}

func (w *blockingACPWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	<-w.unblock
	return len(p), nil
}

func TestRunACPCancellationInterruptsBlockedOutput(t *testing.T) {
	stream := newFakeACPStream(acpEvents(
		&msb.ExecEvent{Kind: msb.ExecEventStdout, Data: []byte("protocol chunk")},
	)...)
	runtime := NewMicrosandboxRuntime(Config{Harness: HarnessOpenCode, Isolation: IsolationFull})
	runtime.Client = &fakeACPStreamClient{fakeMSBClient: &fakeMSBClient{}, stream: stream}
	writer := newBlockingACPWriter()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runtime.RunACP(ctx, io.NopCloser(strings.NewReader("")), writer, &bytes.Buffer{})
	}()

	<-writer.started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunACP error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		close(writer.unblock)
		<-done
		t.Fatal("RunACP remained blocked in client output after cancellation")
	}
	close(writer.unblock)
	if kills, closes := stream.state(); kills != 1 || closes != 1 {
		t.Fatalf("stream kills/closes = %d/%d, want 1/1 after blocked output cancellation", kills, closes)
	}
}

type failingACPWriter struct{ err error }

func (w failingACPWriter) Write([]byte) (int, error) { return 0, w.err }

func TestRunACPOutputFailureKillsGuest(t *testing.T) {
	writeErr := errors.New("client output closed")
	stream := newFakeACPStream(acpEvents(
		&msb.ExecEvent{Kind: msb.ExecEventStdout, Data: []byte("protocol chunk")},
	)...)
	runtime := NewMicrosandboxRuntime(Config{Harness: HarnessOpenCode, Isolation: IsolationFull})
	runtime.Client = &fakeACPStreamClient{fakeMSBClient: &fakeMSBClient{}, stream: stream}

	err := runtime.RunACP(context.Background(), io.NopCloser(strings.NewReader("")), failingACPWriter{err: writeErr}, &bytes.Buffer{})
	if !errors.Is(err, writeErr) {
		t.Fatalf("RunACP error = %v, want output failure", err)
	}
	if kills, closes := stream.state(); kills != 1 || closes != 1 {
		t.Fatalf("stream kills/closes = %d/%d, want 1/1 after output failure", kills, closes)
	}
}

type failingACPReader struct{ err error }

func (r failingACPReader) Read([]byte) (int, error) { return 0, r.err }

func (r failingACPReader) Close() error { return nil }

func TestRunACPInputFailureKillsGuest(t *testing.T) {
	readErr := errors.New("client input failed")
	stream := newFakeACPStream()
	runtime := NewMicrosandboxRuntime(Config{Harness: HarnessOpenCode, Isolation: IsolationFull})
	runtime.Client = &fakeACPStreamClient{fakeMSBClient: &fakeMSBClient{}, stream: stream}

	err := runtime.RunACP(context.Background(), failingACPReader{err: readErr}, &bytes.Buffer{}, &bytes.Buffer{})
	if !errors.Is(err, readErr) {
		t.Fatalf("RunACP error = %v, want input failure", err)
	}
	if kills, closes := stream.state(); kills != 1 || closes != 1 {
		t.Fatalf("stream kills/closes = %d/%d, want 1/1 after input failure", kills, closes)
	}
}

func TestRunACPReportsGuestStartFailure(t *testing.T) {
	stream := newFakeACPStream(acpEvents(
		&msb.ExecEvent{Kind: msb.ExecEventFailed, Failure: &msb.ExecFailure{Message: "opencode missing"}},
	)...)
	runtime := NewMicrosandboxRuntime(Config{Harness: HarnessOpenCode, Isolation: IsolationFull})
	runtime.Client = &fakeACPStreamClient{fakeMSBClient: &fakeMSBClient{}, stream: stream}

	err := runtime.RunACP(context.Background(), io.NopCloser(strings.NewReader("")), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "opencode missing") {
		t.Fatalf("RunACP error = %v, want structured guest start failure", err)
	}
	if kills, closes := stream.state(); kills != 0 || closes != 1 {
		t.Fatalf("stream kills/closes = %d/%d, want 0/1 when no process started", kills, closes)
	}
}

func TestRunACPRejectsUnsupportedHarnessAndIsolation(t *testing.T) {
	for _, test := range []struct {
		name      string
		harness   Harness
		isolation Isolation
	}{
		{name: "Pi", harness: HarnessPi, isolation: IsolationFull},
		{name: "backend", harness: HarnessOpenCode, isolation: IsolationBackend},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeACPStreamClient{fakeMSBClient: &fakeMSBClient{}, stream: newFakeACPStream()}
			runtime := NewMicrosandboxRuntime(Config{Harness: test.harness, Isolation: test.isolation})
			runtime.Client = client
			err := runtime.RunACP(context.Background(), io.NopCloser(strings.NewReader("")), &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil {
				t.Fatal("RunACP succeeded for an unsupported configuration")
			}
			if client.calls != 0 {
				t.Fatalf("ExecStream calls = %d, want 0", client.calls)
			}
		})
	}
}

func TestRunACPRequiresStreamingClient(t *testing.T) {
	runtime := NewMicrosandboxRuntime(Config{Harness: HarnessOpenCode, Isolation: IsolationFull})
	runtime.Client = &fakeMSBClient{}
	if err := runtime.RunACP(context.Background(), io.NopCloser(strings.NewReader("")), &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("RunACP succeeded without the Microsandbox streaming capability")
	}
}
