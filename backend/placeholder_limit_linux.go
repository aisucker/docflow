package main

import (
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
)

func limitWorkerMemory() error {
	return unix.Setrlimit(unix.RLIMIT_DATA, &unix.Rlimit{Cur: 256 << 20, Max: 256 << 20})
}
func attachWorkerLimit(process *os.Process) (func(), error) { return func() {}, nil }
func hideOfficeProcess(cmd *exec.Cmd)                       {}
