package platform

import "os"

// On Linux os.Executable reads /proc/self/exe, the kernel's link to the
// file of the running image.
func runningExecutable() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	return p
}
