package platform

// fdCeiling is the highest soft limit of open files a process may set: the
// hard limit.
func fdCeiling(hard uint64) uint64 { return hard }
