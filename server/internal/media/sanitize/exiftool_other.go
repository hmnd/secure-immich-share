//go:build !linux

package sanitize

import "syscall"

func workerSysProcAttr() *syscall.SysProcAttr { return nil }
