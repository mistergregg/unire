//go:build windows

package server

import "errors"

// diskFree is not available on Windows (Unire runs in a Linux container).
func diskFree(string) (free, total uint64, err error) {
	return 0, 0, errors.New("not available on this system")
}
