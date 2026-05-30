//go:build e2e && (darwin || linux)

package harness

import "syscall"

// procAttrNewPgrp puts the subprocess in its own process group on Unix so any
// child processes it might spawn are isolated from the test runner.
func procAttrNewPgrp() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
