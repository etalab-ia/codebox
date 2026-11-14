//go:build !windows

package codebox

import "golang.org/x/sys/unix"

// diskFree reports the free bytes of the filesystem containing path.
func diskFree(path string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
