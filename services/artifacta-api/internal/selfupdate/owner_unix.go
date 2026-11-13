//go:build unix

package selfupdate

import (
	"errors"
	"os"
	"syscall"
)

// preserveOwner gives path the owner of orig when running as root (only root
// can chown). Best effort: a failure leaves the root-owned file, which still runs.
func preserveOwner(path string, orig os.FileInfo) {
	st, ok := orig.Sys().(*syscall.Stat_t)
	if !ok || os.Geteuid() != 0 {
		return
	}
	_ = os.Chown(path, int(st.Uid), int(st.Gid))
}

// IsReadOnlyFS reports a read-only filesystem (e.g. a Nix store), which needs
// the installer rather than an in-place swap.
func IsReadOnlyFS(err error) bool { return errors.Is(err, syscall.EROFS) }
