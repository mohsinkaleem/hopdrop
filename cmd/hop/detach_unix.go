//go:build unix

package main

import "syscall"

// detached runs the background daemon in its own session, so closing the terminal does not stop it.
func detached() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
