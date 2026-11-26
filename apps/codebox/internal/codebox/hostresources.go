package codebox

import (
	"fmt"
	"math/big"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

const GuestMemoryWarningPercent = 80
const defaultGuestMemoryPercent = 75

var memorySizePattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?$`)

// HostResources describes the CPU and effective memory ceiling visible to
// codebox. On Linux, MemoryMB respects a smaller cgroup limit.
type HostResources struct {
	CPUs     int
	MemoryMB int
}

// DetectHostResources reports the execution host's logical CPUs and memory
// ceiling. A memory error still returns the CPU count for partial guidance.
func DetectHostResources() (HostResources, error) {
	resources := HostResources{CPUs: runtime.NumCPU()}
	bytes, err := hostMemoryBytes()
	if err != nil {
		return resources, err
	}
	resources.MemoryMB = int(bytes / (1024 * 1024))
	return resources, nil
}

// MaxGuestCPUs returns the largest whole CPU count at or below the detected
// host's logical CPU capacity.
func MaxGuestCPUs(hostCPUs int) int {
	if hostCPUs <= 0 {
		return 0
	}
	if hostCPUs > MaxSandboxCPUs {
		return MaxSandboxCPUs
	}
	return hostCPUs
}

// MaxGuestMemoryMB returns the largest whole MiB count at or below the
// detected host memory ceiling.
func MaxGuestMemoryMB(hostMemoryMB int) int {
	if hostMemoryMB <= 0 {
		return 0
	}
	if hostMemoryMB > MaxSandboxMemoryMB {
		return MaxSandboxMemoryMB
	}
	return hostMemoryMB
}

// RecommendedDefaultGuestCPUs keeps at least one logical CPU for the host
// when possible, while preserving the historical two-CPU default on larger
// machines. A single-CPU host is the unavoidable exception.
func RecommendedDefaultGuestCPUs(hostCPUs int) int {
	if hostCPUs <= 1 {
		return 1
	}
	if hostCPUs-1 < DefaultSandboxCPUs {
		return hostCPUs - 1
	}
	return DefaultSandboxCPUs
}

// RecommendedDefaultGuestMemoryMB keeps 25% of detected host memory in
// reserve when the historical 4 GiB default would use a larger share.
func RecommendedDefaultGuestMemoryMB(hostMemoryMB int) int {
	if hostMemoryMB <= 0 {
		return DefaultSandboxMemoryMB
	}
	capacityDefault := hostMemoryMB/100*defaultGuestMemoryPercent + hostMemoryMB%100*defaultGuestMemoryPercent/100
	if capacityDefault < 1 {
		capacityDefault = 1
	}
	if capacityDefault < DefaultSandboxMemoryMB {
		return capacityDefault
	}
	return DefaultSandboxMemoryMB
}

// ValidateGuestResources rejects a guest allocation above detected host
// capacity. The setting is a hard ceiling, not a recommendation.
func ValidateGuestResources(host HostResources, guestCPUs, guestMemoryMB int) error {
	if host.CPUs <= 0 || host.MemoryMB <= 0 {
		return fmt.Errorf("impossible d'appliquer la limite de ressources de la sandbox : la capacité du poste n'a pas pu être détectée")
	}
	if guestCPUs < 1 {
		return fmt.Errorf("la demande de processeurs de la sandbox doit être d'au moins 1, obtenu %d", guestCPUs)
	}
	if guestMemoryMB < 1 {
		return fmt.Errorf("la demande de mémoire de la sandbox doit être d'au moins 1 MiB, obtenu %d", guestMemoryMB)
	}
	maxCPUs := MaxGuestCPUs(host.CPUs)
	if maxCPUs < 1 {
		return fmt.Errorf("aucun processeur logique du poste n'est disponible pour la sandbox")
	}
	if guestCPUs > maxCPUs {
		return fmt.Errorf("la demande de %d processeurs dépasse la capacité détectée du poste (%d processeurs logiques)", guestCPUs, maxCPUs)
	}
	maxMemory := MaxGuestMemoryMB(host.MemoryMB)
	if guestMemoryMB > maxMemory {
		return fmt.Errorf("la demande de mémoire %s dépasse la capacité détectée du poste (%s)", FormatMemorySize(guestMemoryMB), FormatMemorySize(maxMemory))
	}
	return nil
}

// GuestResourceWarnings returns non-fatal review warnings for configurations
// that consume all logical CPUs or at least 80% of detected host memory.
func GuestResourceWarnings(host HostResources, guestCPUs, guestMemoryMB int) []string {
	if host.CPUs <= 0 || host.MemoryMB <= 0 {
		return nil
	}
	var warnings []string
	if guestCPUs >= host.CPUs {
		warnings = append(warnings, fmt.Sprintf("la sandbox utilisera les %d processeurs logiques du poste ; les autres applications peuvent ralentir", host.CPUs))
	}
	if int64(guestMemoryMB)*100 >= int64(host.MemoryMB)*GuestMemoryWarningPercent {
		percent := float64(guestMemoryMB) * 100 / float64(host.MemoryMB)
		warnings = append(warnings, fmt.Sprintf("la mémoire de la sandbox (%s) utilise %.1f %% de la mémoire détectée sur ce poste ; le système et la virtualisation risquent de manquer de marge", FormatMemorySize(guestMemoryMB), percent))
	}
	return warnings
}

// ParseMemorySize parses a positive whole number of MiB or a GiB value such
// as "4096", "4G", or "2.5G". G/GB/GiB are binary GiB (1024 MiB); fractional
// GiB is rounded down to the nearest MiB so it cannot exceed a capacity cap.
func ParseMemorySize(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("la taille de mémoire est vide")
	}
	upper := strings.ToUpper(value)
	unit := "MiB"
	numeric := upper
	factor := int64(1)
	for _, suffix := range []string{"GIB", "GB", "G"} {
		if strings.HasSuffix(upper, suffix) {
			numeric = strings.TrimSpace(strings.TrimSuffix(upper, suffix))
			unit = "GiB"
			factor = 1024
			break
		}
	}
	if !memorySizePattern.MatchString(numeric) {
		return 0, fmt.Errorf("doit être un nombre de MiB positif ou une taille en GiB telle que 4G ou 2.5G, obtenu %q", value)
	}
	r, ok := new(big.Rat).SetString(numeric)
	if !ok || r.Sign() <= 0 {
		return 0, fmt.Errorf("doit être supérieur à zéro, obtenu %q", value)
	}
	r.Mul(r, new(big.Rat).SetInt64(factor))
	mb := new(big.Int).Quo(r.Num(), r.Denom())
	if !mb.IsInt64() || mb.Int64() > int64(MaxSandboxMemoryMB) {
		return 0, fmt.Errorf("ne doit pas dépasser %d MiB", MaxSandboxMemoryMB)
	}
	if unit == "MiB" && r.Denom().Cmp(big.NewInt(1)) != 0 {
		return 0, fmt.Errorf("les valeurs en MiB doivent être des nombres entiers, obtenu %q", value)
	}
	parsed := int(mb.Int64())
	if parsed < 1 {
		return 0, fmt.Errorf("doit être d'au moins 1 MiB, obtenu %q", value)
	}
	return parsed, nil
}

// FormatMemorySize renders memory compactly while keeping uncommon sizes
// exact in MiB (shown as Mo). The short G suffix is binary (GiB).
func FormatMemorySize(memoryMB int) string {
	if memoryMB > 0 && memoryMB%256 == 0 {
		quarterGiB := memoryMB / 256
		fraction := []string{"", ".25", ".5", ".75"}[quarterGiB%4]
		return strconv.Itoa(quarterGiB/4) + fraction + "G"
	}
	return strconv.Itoa(memoryMB) + " Mo"
}

// BrowserResourceGuidance reports the requested guest sizing in host context
// without inventing a browser workload peak that has not been measured.
func BrowserResourceGuidance(host HostResources, guestCPUs, guestMemoryMB int, detectionErr error) string {
	if guestCPUs <= 0 {
		guestCPUs = DefaultSandboxCPUs
	}
	if guestMemoryMB <= 0 {
		guestMemoryMB = DefaultSandboxMemoryMB
	}
	message := fmt.Sprintf("Navigateur dans la sandbox : %d vCPU, %d Mo de RAM, 8 Go de disque. Mesure P03 : environ 2,6 Go utilisés. Le pic de mémoire du navigateur n'a pas été mesuré : aucune augmentation de mémoire n'est justifiée.", guestCPUs, guestMemoryMB)
	if host.CPUs > 0 {
		message += fmt.Sprintf(" Poste : %d processeurs logiques", host.CPUs)
	}
	if host.MemoryMB > 0 {
		message += fmt.Sprintf(" / %d Mo de mémoire au maximum", host.MemoryMB)
	}
	if detectionErr != nil {
		message += ". Mémoire du poste non détectée : sa capacité est inconnue"
	} else if host.CPUs > 0 && host.MemoryMB > 0 {
		message += ". La capacité détectée est un maximum, qui ne tient pas compte des autres applications"
	}
	return message
}
