package sysenv

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"unsafe"
)

// KillTreeOnCancel makes context cancellation terminate the whole process
// tree. Agent CLIs are often .cmd shims, so killing only cmd.exe would leave
// the real agent running and editing the worktree.
func KillTreeOnCancel(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		systemRoot := os.Getenv("SystemRoot")
		if systemRoot == "" {
			systemRoot = `C:\Windows`
		}
		taskkill := filepath.Join(systemRoot, "System32", "taskkill.exe")
		kill := exec.Command(taskkill, "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
		if err := kill.Run(); err != nil {
			// Restricted Windows accounts can be denied access by taskkill
			// even when they can terminate processes they started.
			childrenErr := killDescendants(uint32(cmd.Process.Pid))
			rootErr := cmd.Process.Kill()
			if childrenErr == nil {
				return rootErr
			}
			return errors.Join(fmt.Errorf("terminate agent descendants: %w", childrenErr), rootErr)
		}
		return nil
	}
}

func killDescendants(root uint32) error {
	snapshot, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(snapshot)
	children := make(map[uint32][]uint32)
	entry := syscall.ProcessEntry32{Size: uint32(unsafe.Sizeof(syscall.ProcessEntry32{}))}
	for err = syscall.Process32First(snapshot, &entry); err == nil; err = syscall.Process32Next(snapshot, &entry) {
		children[entry.ParentProcessID] = append(children[entry.ParentProcessID], entry.ProcessID)
	}
	if !errors.Is(err, syscall.ERROR_NO_MORE_FILES) {
		return err
	}
	const queryLimitedInformation = 0x1000
	process, err := syscall.OpenProcess(queryLimitedInformation, false, root)
	if err != nil {
		return fmt.Errorf("open agent process %d: %w", root, err)
	}
	defer syscall.CloseHandle(process)
	started, err := processCreationTime(process)
	if err != nil {
		return err
	}
	// PID reuse can make the parent map cyclic (e.g. equal creation times or
	// PID 0 listing itself), so never visit a process twice.
	visited := map[uint32]bool{root: true}
	var terminate func(uint32, int64) error
	terminate = func(parent uint32, parentStarted int64) error {
		var result error
		for _, pid := range children[parent] {
			if visited[pid] {
				continue
			}
			visited[pid] = true
			child, err := syscall.OpenProcess(queryLimitedInformation|syscall.PROCESS_TERMINATE, false, pid)
			if err != nil {
				// OpenProcess reports ERROR_INVALID_PARAMETER for a vanished PID.
				if !errors.Is(err, syscall.Errno(87)) {
					result = errors.Join(result, fmt.Errorf("open descendant process %d: %w", pid, err))
				}
				continue
			}
			started, err := processCreationTime(child)
			// A process older than its reported parent belongs to a
			// previous process that had the same parent PID.
			if err == nil && started >= parentStarted {
				err = errors.Join(terminate(pid, started), syscall.TerminateProcess(child, 1))
			}
			if err = errors.Join(err, syscall.CloseHandle(child)); err != nil {
				result = errors.Join(result, fmt.Errorf("kill process %d: %w", pid, err))
			}
		}
		return result
	}
	return terminate(root, started)
}

func processCreationTime(process syscall.Handle) (int64, error) {
	var created, exited, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(process, &created, &exited, &kernel, &user); err != nil {
		return 0, fmt.Errorf("read process creation time: %w", err)
	}
	return created.Nanoseconds(), nil
}
