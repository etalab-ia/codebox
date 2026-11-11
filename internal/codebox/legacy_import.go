package codebox

import (
	"bufio"
	"fmt"
	"sort"
	"strings"
)

// Legacy .env import (P04). The new resolution path never reads a .env
// implicitly; this helper supports the explicit, bounded import offered to
// existing users. It reuses parseDotenv (the same parser the legacy path
// uses) and only maps recognized keys, so an import cannot smuggle unknown
// or secret-shaped variables into the managed schema.

// legacyEnvKeyMap maps the documented legacy .env keys to resolver field
// names. ALBERT_API_KEY is deliberately absent: credentials follow the P08
// store, and the import reports it as advice instead of writing a literal
// anywhere.
var legacyEnvKeyMap = map[string]string{
	"RUNTIME":                  "runtime",
	"ISOLATION":                "isolation",
	"WORKSPACE_DIR":            "workspace_dir",
	"PROJECT_DIR":              "workspace_dir", // deprecated alias
	"OPENCODE_SERVER_USERNAME": "username",
	"TART_IMAGE":               "tart_image",
	"TART_MTU":                 "tart_mtu",
	"AGENT_VM_TEMPLATE":        "agent_vm_template",
	"AGENT_VM_VM":              "agent_vm_vm",
	"AGENT_VM_IMAGE":           "agent_vm_image",
	"AGENT_VM_DISK_GB":         "agent_vm_disk_gb",
	"AGENT_VM_MEMORY_GB":       "agent_vm_memory_gb",
	"AGENT_VM_CPUS":            "agent_vm_cpus",
	"CODEBOX_START_TIMEOUT":    "start_timeout",
}

// LegacyImport is the result of a bounded legacy .env import.
type LegacyImport struct {
	// Keys lists unique source variable names; values are never included.
	Keys []string
	// Mapped holds resolver field -> value for recognized non-secret keys.
	Mapped map[string]string
	// Unrecognized lists the .env keys the managed schema does not carry.
	Unrecognized []string
	// CredentialAdvice is non-empty when the .env held ALBERT_API_KEY or
	// another credential-shaped key: the import never copies the value.
	CredentialAdvice string
}

// ImportLegacyDotenv parses legacy .env content and maps recognized keys to
// resolver fields. It never returns a credential value; a credential-shaped
// key sets CredentialAdvice instead. Originals are not touched: the caller
// owns the file and decides what to do after reviewing the preview.
// Duplicate keys keep their first definition, matching the legacy loader's
// "already-exported variables win" behavior.
func ImportLegacyDotenv(content string) LegacyImport {
	imp := LegacyImport{Mapped: map[string]string{}}
	seenKeys := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(content))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		k := strings.TrimSpace(line[:eq])
		if !seenKeys[k] {
			seenKeys[k] = true
			imp.Keys = append(imp.Keys, k)
		}
		v := strings.TrimSpace(line[eq+1:])
		if len(v) >= 2 {
			if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
				v = v[1 : len(v)-1]
			}
		}
		if field, ok := legacyEnvKeyMap[k]; ok {
			if _, exists := imp.Mapped[field]; !exists {
				imp.Mapped[field] = v
			}
			continue
		}
		if isCredentialKey(k) {
			if imp.CredentialAdvice == "" {
				imp.CredentialAdvice = fmt.Sprintf("%s is a credential; import it explicitly into the credential store (auth add), never into a managed file", k)
			}
			continue
		}
		// Report each unrecognized key once, even when duplicated.
		seen := false
		for _, u := range imp.Unrecognized {
			if u == k {
				seen = true
				break
			}
		}
		if !seen {
			imp.Unrecognized = append(imp.Unrecognized, k)
		}
	}
	sort.Strings(imp.Keys)
	sort.Strings(imp.Unrecognized)
	return imp
}

// ReadLegacyAlbertCredential extracts only a literal ALBERT_API_KEY
// assignment from a project .env file. It performs no shell expansion,
// command substitution, profile loading, or file writes. The returned value
// must be passed directly to the credential store and never displayed.
func ReadLegacyAlbertCredential(content string) (string, bool) {
	value, available, _ := inspectLegacyAlbertCredential(content)
	return value, available
}

// LegacyAlbertCredentialNeedsManualReview reports an unsupported or repeated
// assignment without revealing its value.
func LegacyAlbertCredentialNeedsManualReview(content string) bool {
	_, _, review := inspectLegacyAlbertCredential(content)
	return review
}

func inspectLegacyAlbertCredential(content string) (string, bool, bool) {
	sc := bufio.NewScanner(strings.NewReader(content))
	sc.Buffer(make([]byte, 64*1024), maxImportFileSize)
	found, review := false, false
	var value string
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexByte(line, '=')
		if eq <= 0 || strings.TrimSpace(line[:eq]) != "ALBERT_API_KEY" {
			continue
		}
		if found {
			review = true
			continue
		}
		found = true
		candidate := strings.TrimSpace(line[eq+1:])
		if len(candidate) >= 2 && ((candidate[0] == '"' && candidate[len(candidate)-1] == '"') || (candidate[0] == '\'' && candidate[len(candidate)-1] == '\'')) {
			candidate = candidate[1 : len(candidate)-1]
		}
		if candidate == "" || strings.ContainsAny(candidate, " \t\r\n$`\\;|&()#'\"") {
			review = true
			continue
		}
		value = candidate
	}
	if sc.Err() != nil {
		review = true
	}
	if !found {
		return "", false, review
	}
	if review || value == "" {
		return "", false, true
	}
	return value, true, false
}

// isCredentialKey reports whether a legacy key looks like a credential. The
// known key is ALBERT_API_KEY; anything matching *KEY/*TOKEN/*SECRET/*PASSWORD
// is treated as credential-shaped too, so an unknown secret-holding variable
// cannot slip into the mapped set.
func isCredentialKey(k string) bool {
	if k == "ALBERT_API_KEY" {
		return true
	}
	u := strings.ToUpper(k)
	return strings.HasSuffix(u, "_KEY") || strings.HasSuffix(u, "_TOKEN") ||
		strings.HasSuffix(u, "_SECRET") || strings.HasSuffix(u, "_PASSWORD")
}

// FormatLegacyImport renders an import preview: the mapped values, the
// unrecognized keys, and credential advice. It contains no secret bytes
// because Mapped never holds any.
func (imp LegacyImport) FormatLegacyImport() string {
	var b strings.Builder
	if len(imp.Mapped) > 0 {
		b.WriteString("Mapped to managed fields:\n")
		keys := make([]string, 0, len(imp.Mapped))
		for k := range imp.Mapped {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "  %s = %s\n", k, imp.Mapped[k])
		}
	}
	if len(imp.Unrecognized) > 0 {
		b.WriteString("Not carried over (no managed equivalent):\n")
		for _, k := range imp.Unrecognized {
			fmt.Fprintf(&b, "  %s\n", k)
		}
	}
	if imp.CredentialAdvice != "" {
		fmt.Fprintf(&b, "Credential: %s\n", imp.CredentialAdvice)
	}
	if b.Len() == 0 {
		b.WriteString("Nothing to import.\n")
	}
	return b.String()
}
