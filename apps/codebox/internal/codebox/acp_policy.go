package codebox

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

const acpGuestWorkspace = "/workspace"

// NewACPPolicyReader wraps the client input with the codebox ACP boundary.
// The returned reader owns the same close contract as the underlying input:
// Close must unblock Read so RunACP can join the input copier during cleanup.
func NewACPPolicyReader(input io.ReadCloser) io.ReadCloser {
	return &acpPolicyReader{source: bufio.NewReader(input), closeInput: input.Close}
}

type acpPolicyReader struct {
	source      *bufio.Reader
	closeInput  func() error
	pending     []byte
	terminalErr error
}

func (r *acpPolicyReader) Read(p []byte) (int, error) {
	for {
		if len(r.pending) > 0 {
			n := copy(p, r.pending)
			r.pending = r.pending[n:]
			if len(r.pending) == 0 && r.terminalErr != nil {
				err := r.terminalErr
				r.terminalErr = nil
				return n, err
			}
			return n, nil
		}
		if r.terminalErr != nil {
			err := r.terminalErr
			r.terminalErr = nil
			return 0, err
		}

		line, err := r.source.ReadBytes('\n')
		if len(line) == 0 {
			return 0, err
		}
		transformed, policyErr := mediateACPClientMessage(line)
		if policyErr != nil {
			return 0, policyErr
		}
		r.pending = transformed
		if err != nil {
			r.terminalErr = err
		}
	}
}

func (r *acpPolicyReader) Close() error {
	return r.closeInput()
}

// mediateACPClientMessage rewrites only the requests that carry codebox policy.
// Unknown methods and responses keep their original bytes. Raw JSON fields avoid
// coercing JSON-RPC IDs or extension values through float64.
func mediateACPClientMessage(line []byte) ([]byte, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(line, &envelope); err != nil {
		return nil, fmt.Errorf("message ACP client mal formé")
	}
	methodRaw, ok := envelope["method"]
	if !ok {
		return line, nil
	}
	var method string
	if err := json.Unmarshal(methodRaw, &method); err != nil {
		return nil, fmt.Errorf("méthode ACP client invalide")
	}

	var err error
	switch method {
	case "initialize":
		err = mediateACPInitialize(envelope)
	case "session/new", "session/load", "session/resume", "session/fork":
		err = mediateACPSession(envelope, true)
	case "session/list":
		err = mediateACPSession(envelope, false)
	default:
		return line, nil
	}
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("sérialisation du message ACP médiateur : %w", err)
	}
	return append(out, '\n'), nil
}

func mediateACPInitialize(envelope map[string]json.RawMessage) error {
	params, err := acpRequiredObject(envelope, "params")
	if err != nil {
		return err
	}
	capabilitiesRaw, ok := params["clientCapabilities"]
	if !ok || bytes.Equal(bytes.TrimSpace(capabilitiesRaw), []byte("null")) {
		return nil
	}
	var capabilities map[string]json.RawMessage
	if err := json.Unmarshal(capabilitiesRaw, &capabilities); err != nil {
		return fmt.Errorf("clientCapabilities ACP invalide")
	}
	if fsRaw, ok := capabilities["fs"]; ok && !bytes.Equal(bytes.TrimSpace(fsRaw), []byte("null")) {
		var fs map[string]json.RawMessage
		if err := json.Unmarshal(fsRaw, &fs); err != nil {
			return fmt.Errorf("capacités de fichiers hôte ACP invalides")
		}
		fs["readTextFile"] = json.RawMessage("false")
		fs["writeTextFile"] = json.RawMessage("false")
		capabilities["fs"] = acpRawJSON(fs)
	}
	if _, ok := capabilities["terminal"]; ok {
		capabilities["terminal"] = json.RawMessage("false")
	}
	if authRaw, ok := capabilities["auth"]; ok && !bytes.Equal(bytes.TrimSpace(authRaw), []byte("null")) {
		var auth map[string]json.RawMessage
		if err := json.Unmarshal(authRaw, &auth); err != nil {
			return fmt.Errorf("capacités d'authentification ACP invalides")
		}
		auth["terminal"] = json.RawMessage("false")
		capabilities["auth"] = acpRawJSON(auth)
	}
	if metaRaw, ok := capabilities["_meta"]; ok && !bytes.Equal(bytes.TrimSpace(metaRaw), []byte("null")) {
		var meta map[string]json.RawMessage
		if err := json.Unmarshal(metaRaw, &meta); err != nil {
			return fmt.Errorf("métadonnées de capacités ACP invalides")
		}
		if _, ok := meta["terminal-auth"]; ok {
			meta["terminal-auth"] = json.RawMessage("false")
			capabilities["_meta"] = acpRawJSON(meta)
		}
	}
	params["clientCapabilities"] = acpRawJSON(capabilities)
	envelope["params"] = acpRawJSON(params)
	return nil
}

func mediateACPSession(envelope map[string]json.RawMessage, cwdRequired bool) error {
	paramsRaw, ok := envelope["params"]
	if !ok || bytes.Equal(bytes.TrimSpace(paramsRaw), []byte("null")) {
		if cwdRequired {
			return fmt.Errorf("paramètres de session ACP manquants")
		}
		return nil
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(paramsRaw, &params); err != nil {
		return fmt.Errorf("paramètres de session ACP invalides")
	}
	if err := acpSanitizeArray(params, "additionalDirectories"); err != nil {
		return err
	}
	if err := acpSanitizeArray(params, "mcpServers"); err != nil {
		return err
	}

	cwdRaw, ok := params["cwd"]
	if !ok {
		if cwdRequired {
			return fmt.Errorf("le répertoire de travail ACP est requis")
		}
	} else if !bytes.Equal(bytes.TrimSpace(cwdRaw), []byte("null")) {
		var cwd string
		if err := json.Unmarshal(cwdRaw, &cwd); err != nil || (cwdRequired && cwd == "") {
			return fmt.Errorf("répertoire de travail ACP invalide")
		}
		params["cwd"] = json.RawMessage(`"` + acpGuestWorkspace + `"`)
	} else if cwdRequired {
		return fmt.Errorf("le répertoire de travail ACP est requis")
	}
	envelope["params"] = acpRawJSON(params)
	return nil
}

func acpRequiredObject(envelope map[string]json.RawMessage, name string) (map[string]json.RawMessage, error) {
	raw, ok := envelope[name]
	if !ok {
		return nil, fmt.Errorf("%s ACP manquant", name)
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, fmt.Errorf("%s ACP invalide", name)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("%s ACP invalide", name)
	}
	return object, nil
}

func acpSanitizeArray(params map[string]json.RawMessage, name string) error {
	raw, ok := params[name]
	if !ok {
		return nil
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		return fmt.Errorf("%s doit être un tableau dans le périmètre codebox", name)
	}
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return fmt.Errorf("%s ACP invalide", name)
	}
	params[name] = json.RawMessage("[]")
	return nil
}

func acpRawJSON(value any) json.RawMessage {
	out, _ := json.Marshal(value)
	return out
}
