package codebox

import (
	"strings"
	"testing"
)

func TestValidatePortMappings(t *testing.T) {
	tests := []struct {
		name    string
		ports   []PortMapping
		wantErr bool
	}{
		{"valid and sorted", []PortMapping{{Host: 8080, Guest: 80}, {Host: 3000, Guest: 3000}}, false},
		{"zero host", []PortMapping{{Host: 0, Guest: 80}}, true},
		{"zero guest", []PortMapping{{Host: 80, Guest: 0}}, true},
		{"reserved host", []PortMapping{{Host: DefaultPort, Guest: 80}}, true},
		{"reserved guest", []PortMapping{{Host: 8080, Guest: DefaultPort}}, true},
		{"duplicate host", []PortMapping{{Host: 8080, Guest: 80}, {Host: 8080, Guest: 81}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePortMappings(tt.ports)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidatePortMappings() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && tt.ports[0].Host > tt.ports[len(tt.ports)-1].Host {
				t.Fatalf("mappings not sorted: %+v", tt.ports)
			}
		})
	}
}

func TestWriteProjectPortsPreservesUnknownManifestFields(t *testing.T) {
	fs := newMapFS()
	path := "/project/.codebox/project.json"
	fs.files[path] = []byte(`{"schemaVersion":4,"futureField":{"keep":true},"runtime":"microsandbox"}`)
	if err := WriteProjectPorts(fs, path, []PortMapping{{Host: 8080, Guest: 80}}); err != nil {
		t.Fatal(err)
	}
	got := string(fs.files[path])
	for _, want := range []string{`"schemaVersion": 6`, `"portsConfigured": true`, `"host": 8080`, `"guest": 80`, `"futureField"`} {
		if !strings.Contains(got, want) {
			t.Errorf("updated manifest missing %q: %s", want, got)
		}
	}
}

func TestPortChangesRequireRecreate(t *testing.T) {
	applied := InstanceState{SchemaVersion: instanceStateSchemaVersion, Instance: "p", Isolation: string(IsolationFull), Image: "image", ConfigRevision: "same"}
	desired := DesiredState{Instance: "p", Isolation: IsolationFull, Image: "image", PortsConfigured: true, Ports: []PortMapping{{Host: 8080, Guest: 80}}}
	applied.ConfigRevision = desired.ConfigRevision()
	facts := ReconcileFacts{Exists: true, Running: true, Healthy: true, CreationFixedChanged: true}
	plan := PlanReconcile(&applied, desired, facts)
	if !plan.NeedsRecreate() {
		t.Fatalf("port configuration change should require explicit recreation, got %+v", plan)
	}
	if plan.Reason == "" {
		t.Fatal("recreation plan should explain why")
	}
	if !portMappingsChanged(applied.PortsConfigured, applied.Ports, desired.Ports, desired.PortsConfigured) {
		t.Fatal("changed mapping should be detected as creation-fixed")
	}
}

func TestLegacyPortStateDoesNotRequestRecreation(t *testing.T) {
	applied := InstanceState{SchemaVersion: instanceStateSchemaVersion, Instance: "p", Isolation: string(IsolationFull), Image: "image", ConfigRevision: "same"}
	desired := DesiredState{Instance: "p", Isolation: IsolationFull, Image: "image"}
	applied.ConfigRevision = desired.ConfigRevision()
	facts := ReconcileFacts{Exists: true, Running: true, Healthy: true}
	if plan := PlanReconcile(&applied, desired, facts); !plan.IsNoOp() {
		t.Fatalf("legacy default port configuration should remain compatible, got %+v", plan)
	}
	if portMappingsChanged(applied.PortsConfigured, applied.Ports, desired.Ports, desired.PortsConfigured) {
		t.Fatal("unchanged legacy port state must not require recreation")
	}
}
