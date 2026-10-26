// Package privdir creates the server's working directories under a shared
// $TMPDIR safely. In the container /tmp is private, but the binary can also
// run on a multi-user host, where another user could create the directory
// first (or a symlink in its place) and then read the Helm registry
// credentials, swap a git clone before it is applied, or poison the Trivy
// cache. Ensure refuses anything that is not a real directory owned by us
// and closed to everyone else.
package privdir

import (
	"fmt"
	"os"
	"syscall"
)

// Ensure creates dir with mode 0700 if missing and fails unless it is a
// directory (not a symlink) owned by the current user with no group/other
// permissions.
func Ensure(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return err
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || !ok || int(sys.Uid) != os.Getuid() || st.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s must be a directory owned by this user with mode 0700; refusing to use it (another user may have created it)", dir)
	}
	return nil
}
