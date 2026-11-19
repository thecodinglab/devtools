package platform

import (
	"bytes"
	"encoding/binary"
)

// parseProcargs2 reads the buffer of sysctl kern.procargs2 on macOS: argc as
// a 32-bit integer in the machine's byte order (little-endian on both Mac
// architectures), the executable path ending in NUL, NUL padding, then
// argc arguments each ending in NUL, then the environment, which is not
// read. A buffer that is too short gives what it holds.
//
// The layout is the one of the xnu sources (sysctl_procargsx); it is
// provisional until E2 part 2 reads a real buffer on a Mac. The file has no
// build tag so its test runs on Linux against a buffer built from that
// layout.
func parseProcargs2(b []byte) []string {
	if len(b) < 4 {
		return nil
	}
	argc := int(int32(binary.LittleEndian.Uint32(b[:4])))
	b = b[4:]
	// The executable path.
	i := bytes.IndexByte(b, 0)
	if i < 0 {
		return nil
	}
	b = b[i:]
	// The padding after it.
	for len(b) > 0 && b[0] == 0 {
		b = b[1:]
	}
	if argc <= 0 {
		return nil
	}
	args := make([]string, 0, min(argc, 256))
	for len(args) < argc && len(b) > 0 {
		j := bytes.IndexByte(b, 0)
		if j < 0 {
			args = append(args, string(b))
			break
		}
		args = append(args, string(b[:j]))
		b = b[j+1:]
	}
	return args
}
