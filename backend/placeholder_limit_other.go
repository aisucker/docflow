//go:build !windows && !linux

package main

import (
	"fmt"
	"os"
	"os/exec"
)

func limitWorkerMemory() error {
	return fmt.Errorf("Изоляция JavaScript поддерживается на Windows и Linux")
}
func attachWorkerLimit(process *os.Process) (func(), error) { return func() {}, nil }
func hideOfficeProcess(cmd *exec.Cmd)                       {}
