//go:build !darwin && !linux && !windows

package platform

import (
	"errors"
	"os/exec"
)

func isolateProcess(*exec.Cmd) error {
	return errors.New("native stdio process isolation requires macOS or Linux")
}
func terminateProcessGroup(int) {}
