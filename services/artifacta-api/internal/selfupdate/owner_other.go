//go:build !unix

package selfupdate

import "os"

func preserveOwner(string, os.FileInfo) {}

// IsReadOnlyFS is always false where self-update is unsupported anyway.
func IsReadOnlyFS(error) bool { return false }
