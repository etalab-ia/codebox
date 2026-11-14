//go:build linux

package codebox

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func hostMemoryBytes() (uint64, error) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, fmt.Errorf("read host memory: %w", err)
	}
	defer file.Close()

	var total uint64
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 || fields[0] != "MemTotal:" || fields[2] != "kB" {
			continue
		}
		kib, parseErr := strconv.ParseUint(fields[1], 10, 64)
		if parseErr != nil {
			return 0, fmt.Errorf("parse host memory: %w", parseErr)
		}
		total = kib * 1024
		break
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("read host memory: %w", err)
	}
	if total == 0 {
		return 0, fmt.Errorf("host memory total not found in /proc/meminfo")
	}
	return linuxMemoryLimit(total), nil
}

func linuxMemoryLimit(total uint64) uint64 {
	for _, path := range []string{"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes"} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		value := strings.TrimSpace(string(data))
		if value == "" || value == "max" {
			continue
		}
		limit, err := strconv.ParseUint(value, 10, 64)
		if err == nil && limit > 0 && limit < total {
			return limit
		}
	}
	return total
}
