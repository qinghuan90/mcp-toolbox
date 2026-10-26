//go:build !windows

package platform

import (
	"os"
	"os/exec"
	"path/filepath"
)

func registerProcessGroup(int) error             { return nil }
func preparePlatformCommand(*exec.Cmd) error     { return nil }
func virtualPython(venv string) string           { return filepath.Join(venv, "bin", "python") }
func virtualScript(venv, name string) string     { return filepath.Join(venv, "bin", name) }
func runtimeCommand(name string) (string, error) { return exec.LookPath(name) }

func platformProcessEnv(helper bool) []string {
	if helper {
		return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	}
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "TMPDIR=" + os.TempDir(), "LANG=" + os.Getenv("LANG")}
}
