package codebox

import (
	"errors"
	"strings"
	"testing"
)

var errTestHostMemory = errors.New("memory unavailable")

func TestBrowserResourceGuidanceUsesHostCapacityWithoutInventingPeak(t *testing.T) {
	got := BrowserResourceGuidance(HostResources{CPUs: 8, MemoryMB: 16384}, 2, 4096, nil)
	for _, want := range []string{"2 vCPU", "4096 Mo de RAM", "8 Go de disque", "8 processeurs logiques", "16384 Mo de mémoire au maximum", "2,6 Go", "pic de mémoire du navigateur n'a pas été mesuré", "ne tient pas compte des autres applications"} {
		if !strings.Contains(got, want) {
			t.Errorf("resource guidance %q lacks %q", got, want)
		}
	}
}

func TestBrowserResourceGuidanceKeepsCapacityAsContext(t *testing.T) {
	got := BrowserResourceGuidance(HostResources{CPUs: 1, MemoryMB: 2048}, 1, 2048, nil)
	for _, want := range []string{"1 vCPU", "1 processeurs logiques", "2048 Mo de mémoire au maximum", "maximum", "autres applications"} {
		if !strings.Contains(got, want) {
			t.Errorf("resource guidance %q lacks %q", got, want)
		}
	}
}

func TestGuestResourceWarningsAtHighAllocations(t *testing.T) {
	warnings := GuestResourceWarnings(HostResources{CPUs: 8, MemoryMB: 16384}, 8, 13108)
	joined := strings.Join(warnings, "; ")
	if !strings.Contains(joined, "les 8 processeurs logiques du poste") || !strings.Contains(joined, "80.0 % de la mémoire détectée") {
		t.Fatalf("high allocation warnings missing: %q", joined)
	}
}

func TestBrowserResourceGuidanceReportsUnknownMemoryDetection(t *testing.T) {
	got := BrowserResourceGuidance(HostResources{CPUs: 4}, 2, 4096, errTestHostMemory)
	if !strings.Contains(got, "Mémoire du poste non détectée : sa capacité est inconnue") || !strings.Contains(got, "4 processeurs logiques") {
		t.Fatalf("partial host detection guidance = %q", got)
	}
}

func TestMaxGuestResourcesStayWithinHostCapacity(t *testing.T) {
	for _, tc := range []struct {
		hostCPUs int
		want     int
	}{{1, 1}, {2, 2}, {3, 3}, {4, 4}, {8, 8}, {10, 10}, {256, MaxSandboxCPUs}} {
		if got := MaxGuestCPUs(tc.hostCPUs); got != tc.want {
			t.Errorf("MaxGuestCPUs(%d) = %d, want %d", tc.hostCPUs, got, tc.want)
		}
	}
	if got := MaxGuestMemoryMB(16384); got != 16384 {
		t.Fatalf("MaxGuestMemoryMB(16384) = %d, want 16384", got)
	}
	if got := MaxGuestMemoryMB(MaxSandboxMemoryMB + 1); got != MaxSandboxMemoryMB {
		t.Fatalf("MaxGuestMemoryMB above SDK ceiling = %d, want %d", got, MaxSandboxMemoryMB)
	}
}

func TestRecommendedGuestDefaultsLeaveHostHeadroom(t *testing.T) {
	for _, tc := range []struct {
		hostCPUs int
		want     int
	}{{1, 1}, {2, 1}, {3, 2}, {8, 2}} {
		if got := RecommendedDefaultGuestCPUs(tc.hostCPUs); got != tc.want {
			t.Errorf("RecommendedDefaultGuestCPUs(%d) = %d, want %d", tc.hostCPUs, got, tc.want)
		}
	}
	for _, tc := range []struct {
		hostMemoryMB int
		want         int
	}{{4096, 3072}, {5000, 3750}, {6000, 4096}, {16384, 4096}} {
		if got := RecommendedDefaultGuestMemoryMB(tc.hostMemoryMB); got != tc.want {
			t.Errorf("RecommendedDefaultGuestMemoryMB(%d) = %d, want %d", tc.hostMemoryMB, got, tc.want)
		}
	}
}

func TestValidateGuestResources(t *testing.T) {
	host := HostResources{CPUs: 8, MemoryMB: 16384}
	if err := ValidateGuestResources(host, 8, 16384); err != nil {
		t.Fatalf("request at host capacity rejected: %v", err)
	}
	for _, tc := range []struct {
		name         string
		cpus, memory int
		want         string
	}{{"cpu over cap", 9, 4096, "exceeds detected host capacity"}, {"memory over cap", 4, 16385, "exceeds detected host capacity"}} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateGuestResources(host, tc.cpus, tc.memory); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidateGuestResources() error = %v, want substring %q", err, tc.want)
			}
		})
	}
	if err := ValidateGuestResources(HostResources{CPUs: 1, MemoryMB: 4096}, 1, 2048); err != nil {
		t.Fatalf("single logical CPU should be allowed: %v", err)
	}
	if err := ValidateGuestResources(HostResources{CPUs: 8}, 2, 4096); err == nil || !strings.Contains(err.Error(), "could not be detected") {
		t.Fatalf("unknown host capacity error = %v, want detection error", err)
	}
}

func TestParseAndFormatMemorySize(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  int
	}{{"4096", 4096}, {"4G", 4096}, {"4g", 4096}, {"2.5G", 2560}, {"2.5GiB", 2560}, {"9.6G", 9830}} {
		got, err := ParseMemorySize(tc.input)
		if err != nil || got != tc.want {
			t.Errorf("ParseMemorySize(%q) = %d, %v, want %d", tc.input, got, err, tc.want)
		}
	}
	for _, input := range []string{"", "0", "-1", "1.5", "1e3G", "0.0001G", "999999999999G"} {
		if got, err := ParseMemorySize(input); err == nil {
			t.Errorf("ParseMemorySize(%q) = %d, want error", input, got)
		}
	}
	for _, tc := range []struct {
		memory int
		want   string
	}{{4096, "4G"}, {2560, "2.5G"}, {2304, "2.25G"}, {9830, "9830 Mo"}} {
		if got := FormatMemorySize(tc.memory); got != tc.want {
			t.Errorf("FormatMemorySize(%d) = %q, want %q", tc.memory, got, tc.want)
		}
	}
}
