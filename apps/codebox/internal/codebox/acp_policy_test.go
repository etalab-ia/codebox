package codebox

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func readACPPolicyInput(t *testing.T, input io.Reader) (string, error) {
	t.Helper()
	reader := NewACPPolicyReader(io.NopCloser(input))
	output, err := io.ReadAll(reader)
	closeErr := reader.Close()
	if err == nil {
		err = closeErr
	}
	return string(output), err
}

func decodeACPObject(t *testing.T, input string) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal([]byte(input), &object); err != nil {
		t.Fatalf("decode mediated ACP message: %v\n%s", err, input)
	}
	return object
}

func TestACPPolicyInitializeDisablesHostCapabilities(t *testing.T) {
	input := "{\"jsonrpc\":\"2.0\",\"id\":9007199254740993,\"method\":\"initialize\",\"params\":{\"protocolVersion\":1,\"clientCapabilities\":{\"fs\":{\"readTextFile\":true,\"writeTextFile\":true,\"future\":true},\"terminal\":true,\"auth\":{\"terminal\":true,\"other\":true},\"_meta\":{\"terminal-auth\":true,\"custom\":\"keep\"},\"custom\":42},\"clientInfo\":{\"name\":\"reviewed-client\"}}}\n"
	output, err := readACPPolicyInput(t, strings.NewReader(input))
	if err != nil {
		t.Fatalf("policy read: %v", err)
	}
	message := decodeACPObject(t, output)
	if !strings.Contains(output, `"id":9007199254740993`) {
		t.Fatalf("large JSON-RPC ID was coerced: %s", output)
	}
	params := message["params"].(map[string]any)
	capabilities := params["clientCapabilities"].(map[string]any)
	fs := capabilities["fs"].(map[string]any)
	if fs["readTextFile"] != false || fs["writeTextFile"] != false || fs["future"] != true {
		t.Fatalf("filesystem capabilities = %#v, want host access disabled and extension preserved", fs)
	}
	if capabilities["terminal"] != false {
		t.Fatalf("terminal capability = %#v, want false", capabilities["terminal"])
	}
	auth := capabilities["auth"].(map[string]any)
	if auth["terminal"] != false || auth["other"] != true {
		t.Fatalf("auth capabilities = %#v, want terminal auth disabled and other preserved", auth)
	}
	meta := capabilities["_meta"].(map[string]any)
	if meta["terminal-auth"] != false || meta["custom"] != "keep" {
		t.Fatalf("auth metadata = %#v, want terminal handoff disabled and custom preserved", meta)
	}
	if capabilities["custom"] != float64(42) || params["clientInfo"].(map[string]any)["name"] != "reviewed-client" {
		t.Fatalf("unrelated initialize fields changed: %s", output)
	}
}

func TestACPPolicySessionUsesGuestWorkspace(t *testing.T) {
	for _, method := range []string{"session/new", "session/load", "session/resume", "session/fork", "session/list"} {
		t.Run(method, func(t *testing.T) {
			input := "{\"jsonrpc\":\"2.0\",\"id\":\"request-id\",\"method\":\"" + method + "\",\"params\":{\"cwd\":\"/host/project\",\"additionalDirectories\":[],\"mcpServers\":[],\"sessionId\":\"session-id\"}}\n"
			output, err := readACPPolicyInput(t, strings.NewReader(input))
			if err != nil {
				t.Fatalf("policy read: %v", err)
			}
			message := decodeACPObject(t, output)
			params := message["params"].(map[string]any)
			if params["cwd"] != acpGuestWorkspace {
				t.Fatalf("cwd = %#v, want %q", params["cwd"], acpGuestWorkspace)
			}
			if params["sessionId"] != "session-id" || len(params["additionalDirectories"].([]any)) != 0 || len(params["mcpServers"].([]any)) != 0 {
				t.Fatalf("unrelated session fields changed: %s", output)
			}
		})
	}
}

func TestACPPolicyStripsAdditionalRootsAndClientMCP(t *testing.T) {
	for _, test := range []struct {
		name  string
		field string
		value string
	}{
		{name: "additional root", field: "additionalDirectories", value: `["/host/other"]`},
		{name: "stdio MCP", field: "mcpServers", value: `[{"name":"host","command":"/host/tool"}]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"session/new\",\"params\":{\"cwd\":\"/host/project\",\"" + test.field + "\":" + test.value + "}}\n"
			output, err := readACPPolicyInput(t, strings.NewReader(input))
			if err != nil {
				t.Fatalf("policy read: %v", err)
			}
			message := decodeACPObject(t, output)
			params := message["params"].(map[string]any)
			if got := len(params[test.field].([]any)); got != 0 {
				t.Fatalf("%s length = %d, want deterministic empty array", test.field, got)
			}
		})
	}
}

func TestACPPolicyRejectsNonArrayClientMCP(t *testing.T) {
	input := "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"session/new\",\"params\":{\"cwd\":\"/host/project\",\"mcpServers\":null}}\n"
	output, err := readACPPolicyInput(t, strings.NewReader(input))
	if err == nil || !strings.Contains(err.Error(), "mcpServers") {
		t.Fatalf("policy output/error = %q / %v, want mcpServers type rejection", output, err)
	}
}

func TestACPPolicyRequiresSessionWorkspace(t *testing.T) {
	input := "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"session/new\",\"params\":{\"mcpServers\":[]}}\n"
	if _, err := readACPPolicyInput(t, strings.NewReader(input)); err == nil || !strings.Contains(err.Error(), "travail") {
		t.Fatalf("missing cwd error = %v, want workspace requirement", err)
	}
}

func TestACPPolicyPassesUnknownTrafficByteForByte(t *testing.T) {
	for _, input := range []string{
		" { \"jsonrpc\" : \"2.0\", \"id\" : 9007199254740993, \"method\" : \"vendor/custom\", \"params\" : { \"cwd\" : \"/host/project\" } } \n",
		"{\"jsonrpc\":\"2.0\",\"id\":9,\"result\":{\"cwd\":\"/host/project\"}}\n",
	} {
		output, err := readACPPolicyInput(t, strings.NewReader(input))
		if err != nil {
			t.Fatalf("policy read: %v", err)
		}
		if output != input {
			t.Fatalf("unknown traffic changed:\ninput  %q\noutput %q", input, output)
		}
	}
}

func TestACPPolicyReadsFragmentedMessages(t *testing.T) {
	input := "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{}}\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"session/new\",\"params\":{\"cwd\":\"/host/project\",\"mcpServers\":[]}}\n"
	output, err := readACPPolicyInput(t, iotest.OneByteReader(strings.NewReader(input)))
	if err != nil {
		t.Fatalf("fragmented policy read: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 2 {
		t.Fatalf("mediated message count = %d, want 2: %q", len(lines), output)
	}
	second := decodeACPObject(t, lines[1])
	if second["params"].(map[string]any)["cwd"] != acpGuestWorkspace {
		t.Fatalf("fragmented session cwd changed incorrectly: %s", output)
	}
}

func TestACPPolicyRejectsMalformedInput(t *testing.T) {
	for _, input := range []string{
		"{\"jsonrpc\":",
		"{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":null}\n",
	} {
		if _, err := readACPPolicyInput(t, strings.NewReader(input)); err == nil {
			t.Fatalf("malformed ACP input was accepted: %q", input)
		}
	}
}

type trackingACPReadCloser struct {
	io.Reader
	closed bool
}

func (r *trackingACPReadCloser) Close() error {
	r.closed = true
	return nil
}

func TestACPPolicyCloseOwnsInput(t *testing.T) {
	input := &trackingACPReadCloser{Reader: strings.NewReader("")}
	reader := NewACPPolicyReader(input)
	if err := reader.Close(); err != nil {
		t.Fatalf("policy close: %v", err)
	}
	if !input.closed {
		t.Fatal("policy close did not close the underlying client input")
	}
}
