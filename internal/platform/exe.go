package platform

// RunningExecutable is the file whose image this process runs, resolved by
// the kernel and not through the path it was started by: "" when that
// cannot be told. The server compares it with the build the profile points
// at (internal/server/update.go), so a symlink the process was started
// through, which may point elsewhere by now, is never taken for it.
func RunningExecutable() string { return runningExecutable() }
