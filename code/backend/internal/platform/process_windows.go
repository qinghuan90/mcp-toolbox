//go:build windows

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var windowsProcessJobs sync.Map

func preparePlatformCommand(cmd *exec.Cmd) error {
	cmd.Env = append(platformProcessEnv(false), cmd.Env...)
	return isolateProcess(cmd)
}

func virtualPython(venv string) string       { return filepath.Join(venv, "Scripts", "python.exe") }
func virtualScript(venv, name string) string { return filepath.Join(venv, "Scripts", name+".exe") }
func runtimeCommand(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil && name == "python3" {
		return exec.LookPath("python")
	}
	return path, err
}

func registerProcessGroup(pid int) error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		windows.CloseHandle(job)
		return err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		windows.CloseHandle(job)
		return err
	}
	err = windows.AssignProcessToJobObject(job, process)
	windows.CloseHandle(process)
	if err != nil {
		windows.CloseHandle(job)
		return err
	}
	// Initialize may already have caused npx to spawn its server. Add those
	// existing descendants too; children created afterwards inherit the job.
	if err = attachExistingDescendants(job, uint32(pid)); err != nil {
		windows.CloseHandle(job)
		return err
	}
	windowsProcessJobs.Store(pid, job)
	return nil
}

func attachExistingDescendants(job windows.Handle, root uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	children := map[uint32][]uint32{}
	err = windows.Process32First(snapshot, &entry)
	for err == nil {
		children[entry.ParentProcessID] = append(children[entry.ParentProcessID], entry.ProcessID)
		err = windows.Process32Next(snapshot, &entry)
	}
	if err != windows.ERROR_NO_MORE_FILES {
		return err
	}
	queue := children[root]
	for len(queue) > 0 {
		pid := queue[0]
		queue = append(queue[1:], children[pid]...)
		process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
		if err == windows.ERROR_INVALID_PARAMETER {
			continue
		} // Already exited.
		if err != nil {
			return err
		}
		var inside int32
		result, _, callErr := windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob").Call(uintptr(process), uintptr(job), uintptr(unsafe.Pointer(&inside)))
		if result == 0 {
			err = callErr
		}
		if err == nil && inside == 0 {
			err = windows.AssignProcessToJobObject(job, process)
		}
		windows.CloseHandle(process)
		if err != nil {
			return err
		}
	}
	return nil
}

func isolateProcess(cmd *exec.Cmd) error {
	// npm's Windows launchers are .cmd scripts. Execute their actual Node
	// entrypoint, keeping every argument separate instead of invoking a shell.
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(cmd.Path), filepath.Ext(cmd.Path)))
	if base == "npx" || base == "npm" {
		resolved, err := exec.LookPath(cmd.Args[0])
		if err != nil {
			return err
		}
		if strings.EqualFold(filepath.Ext(resolved), ".cmd") {
			cli := filepath.Join(filepath.Dir(resolved), "node_modules", "npm", "bin", base+"-cli.js")
			if _, err := os.Stat(cli); err != nil {
				return fmt.Errorf("无法找到 %s 的 Node 入口，请安装官方 Node.js 或使用 node 配合脚本路径: %w", base, err)
			}
			node := filepath.Join(filepath.Dir(resolved), "node.exe")
			if _, err := os.Stat(node); err != nil {
				node, err = exec.LookPath("node.exe")
				if err != nil {
					return err
				}
			}
			cmd.Path = node
			cmd.Args = append([]string{node, cli}, cmd.Args[1:]...)
			cmd.Err = nil
		}
	}
	if ext := strings.ToLower(filepath.Ext(cmd.Path)); ext == ".cmd" || ext == ".bat" {
		return fmt.Errorf("Windows stdio 请配置可执行文件或 node/python 脚本入口，不直接执行 %s 批处理", ext)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	return nil
}

func terminateProcessGroup(pid int) {
	if pid <= 0 {
		return
	}
	if value, ok := windowsProcessJobs.LoadAndDelete(pid); ok {
		// The job retains descendants even after the parent exits. Closing the
		// last job handle also prevents children surviving an application exit.
		_ = windows.CloseHandle(value.(windows.Handle))
		return
	}
	// /T terminates descendants created by package launchers as well as the
	// server. Use the trusted Windows installation, never a PATH executable.
	command := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "taskkill.exe"), "/PID", strconv.Itoa(pid), "/T", "/F")
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = command.Run()
}

func platformProcessEnv(helper bool) []string {
	keys := []string{"PATH", "HOME", "USERPROFILE", "HOMEDRIVE", "HOMEPATH", "SystemRoot", "WINDIR", "COMSPEC", "PATHEXT", "APPDATA", "LOCALAPPDATA", "PROGRAMDATA", "ProgramFiles", "ProgramFiles(x86)", "TEMP", "TMP", "LANG"}
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}
