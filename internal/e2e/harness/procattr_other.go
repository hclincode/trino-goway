//go:build e2e && !darwin && !linux

package harness

import "syscall"

// procAttrNewPgrp is a no-op on platforms where Setpgid is unavailable.
func procAttrNewPgrp() *syscall.SysProcAttr {
	return nil
}
