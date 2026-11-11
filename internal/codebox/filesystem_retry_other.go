//go:build !windows

package codebox

func isTransientFilesystemError(error) bool { return false }
