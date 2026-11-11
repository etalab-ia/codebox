package codebox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const mcpProtocolVersion = "2025-03-26"

type MCPHealthState string

const (
	MCPConfigured   MCPHealthState = "configured"
	MCPVerified     MCPHealthState = "verified"
	MCPOffline      MCPHealthState = "offline"
	MCPAuthNeeded   MCPHealthState = "authentication-required"
	MCPAccessDenied MCPHealthState = "access-denied"
	MCPDrift        MCPHealthState = "endpoint-or-schema-drift"
)

type MCPHealth struct {
	Connector MCPConnector
	State     MCPHealthState
	Detail    string
}

// ProbeRemoteMCP performs an MCP initialize handshake, not merely a TCP/HTTP
// reachability check. A successful protocol response marks the remote
// endpoint verified; it does not prove guest DNS or a later tool call.
func ProbeRemoteMCP(ctx context.Context, client *http.Client, connector MCPConnector) MCPHealth {
	result := MCPHealth{Connector: connector, State: MCPConfigured}
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]string{"name": "codebox", "version": "p15"},
		},
	})
	if err != nil {
		result.State, result.Detail = MCPDrift, "could not encode MCP initialize request"
		return result
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, connector.Endpoint, bytes.NewReader(body))
	if err != nil {
		result.State, result.Detail = MCPDrift, "invalid curated endpoint"
		return result
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		result.State, result.Detail = MCPOffline, "remote endpoint could not be reached"
		return result
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		result.State, result.Detail = MCPAuthNeeded, "the remote service requires authentication for this request"
		return result
	}
	if resp.StatusCode == http.StatusForbidden {
		result.State, result.Detail = MCPAccessDenied, "the remote service denied the anonymous request"
		return result
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusUnsupportedMediaType {
		result.State, result.Detail = MCPDrift, fmt.Sprintf("MCP endpoint contract returned HTTP %d", resp.StatusCode)
		return result
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		result.State, result.Detail = MCPOffline, fmt.Sprintf("remote service returned HTTP %d", resp.StatusCode)
		return result
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		result.State, result.Detail = MCPOffline, "could not read MCP response"
		return result
	}
	message, err := mcpResponseJSON(data)
	if err != nil {
		result.State, result.Detail = MCPDrift, "response was not a supported MCP initialize message"
		return result
	}
	var envelope struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	// The request ID is the JSON number 1, so JSON-RPC requires the response
	// to echo that exact value and type.
	if json.Unmarshal(message, &envelope) != nil || envelope.JSONRPC != "2.0" || string(bytes.TrimSpace(envelope.ID)) != "1" {
		result.State, result.Detail = MCPDrift, "response did not match JSON-RPC MCP initialize"
		return result
	}
	// Verify the version we explicitly requested; a different negotiated value
	// is reported as drift until the new version is qualified end to end.
	if envelope.Error != nil || envelope.Result.ProtocolVersion != mcpProtocolVersion {
		result.State, result.Detail = MCPDrift, "server did not negotiate the supported MCP protocol version"
		return result
	}
	result.State = MCPVerified
	result.Detail = "MCP initialize handshake succeeded"
	return result
}

func mcpResponseJSON(data []byte) ([]byte, error) {
	if json.Valid(bytes.TrimSpace(data)) {
		return bytes.TrimSpace(data), nil
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var dataLines []string
	flush := func() ([]byte, bool) {
		if len(dataLines) == 0 {
			return nil, false
		}
		candidate := []byte(strings.Join(dataLines, "\n"))
		dataLines = nil
		if json.Valid(candidate) {
			return candidate, true
		}
		return nil, false
	}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			if candidate, ok := flush(); ok {
				return candidate, nil
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read MCP event stream: %w", err)
	}
	if candidate, ok := flush(); ok {
		return candidate, nil
	}
	return nil, fmt.Errorf("no JSON-RPC data in stream")
}
