//go:build !(darwin || linux)

package hop

import "syscall"

func reuseControl(_, _ string, _ syscall.RawConn) error { return nil }
