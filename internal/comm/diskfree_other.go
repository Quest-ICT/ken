//go:build !linux && !darwin

package comm

// diskFree is unknown on this platform, so the free-space floor is skipped —
// the global storage budget still applies. Skipping is the right degradation:
// failing closed here would break file exchange entirely on platforms whose
// syscall surface we have not wired, over a check that is defense in depth.
func diskFree(string) (int64, bool) { return 0, false }
