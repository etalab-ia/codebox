//go:build windows

package codebox

import (
	"fmt"
	"syscall"
	"unsafe"
)

type memoryStatusEx struct {
	length                   uint32
	memoryLoad               uint32
	totalPhys                uint64
	availablePhys            uint64
	totalPageFile            uint64
	availablePageFile        uint64
	totalVirtual             uint64
	availableVirtual         uint64
	availableExtendedVirtual uint64
}

func hostMemoryBytes() (uint64, error) {
	status := memoryStatusEx{length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")
	result, _, callErr := proc.Call(uintptr(unsafe.Pointer(&status)))
	if result == 0 {
		return 0, fmt.Errorf("query Windows host memory: %w", callErr)
	}
	return status.totalPhys, nil
}
