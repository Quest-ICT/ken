//go:build linux || darwin

package comm

import "syscall"

// diskFree reports the bytes available to unprivileged writes on the filesystem
// holding dir, and whether the answer is known. Bavail (not Bfree) on purpose:
// the root-reserved blocks are exactly the headroom the free-space floor exists
// to protect, so they must not be counted as available.
func diskFree(dir string) (int64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return int64(st.Bavail) * int64(st.Bsize), true
}
