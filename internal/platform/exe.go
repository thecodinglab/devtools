package platform

import "errors"

// RunningExecutable is the file whose image this process runs, resolved by
// the kernel and not through the path it was started by: "" when that
// cannot be told. The server compares it with the build the profile points
// at (internal/server/update.go), so a symlink the process was started
// through, which may point elsewhere by now, is never taken for it.
func RunningExecutable() string { return runningExecutable() }

// SelfExecutable is the identity of RunningExecutable, taken at once: the
// one source of the running image for the watch of the installed build
// and the live upgrade (internal/server/boot). A path that names another
// file than the running image by now (a local build replaced in place) is
// an error where the kernel can tell (runningImage), never the other file.
func SelfExecutable() (Identity, error) {
	p := RunningExecutable()
	if p == "" {
		return Identity{}, errors.New("the file of the running image cannot be told")
	}
	id, err := FileIdentity(p)
	if err != nil {
		return Identity{}, err
	}
	if dev, ino, ok := runningImage(); ok && (dev != id.Dev || ino != id.Ino) {
		return Identity{}, errors.New(p + " is not the file this process runs any more")
	}
	return id, nil
}
