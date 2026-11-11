package sanitize

import "syscall"

// An exiftool left in -stay_open mode polls its closed stdin forever, so
// the kernel kills workers when the server dies.
func workerSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
