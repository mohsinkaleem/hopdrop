//go:build darwin || linux

package hop

import "syscall"

// reuseControl lets several HopDrop instances on one machine share the UDP discovery port.
func reuseControl(_, _ string, c syscall.RawConn) error {
	var serr error
	err := c.Control(func(fd uintptr) {
		if serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); serr == nil {
			serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, soReusePort, 1)
		}
	})
	if err != nil {
		return err
	}
	return serr
}
