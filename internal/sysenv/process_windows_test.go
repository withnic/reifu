package sysenv

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestKillTreeHelperProcess(t *testing.T) {
	switch os.Getenv("REIFU_SYSENV_KILL_HELPER") {
	case "parent":
		child := exec.Command(os.Args[0], "-test.run=^TestKillTreeHelperProcess$")
		child.Env = append(os.Environ(), "REIFU_SYSENV_KILL_HELPER=child")
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		fmt.Println(child.Process.Pid)
		time.Sleep(time.Minute)
		os.Exit(0)
	case "child":
		time.Sleep(time.Minute)
		os.Exit(0)
	}
}

func TestKillDescendantsTerminatesGrandchildren(t *testing.T) {
	parent := exec.Command(os.Args[0], "-test.run=^TestKillTreeHelperProcess$")
	parent.Env = append(os.Environ(), "REIFU_SYSENV_KILL_HELPER=parent")
	stdout, err := parent.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = parent.Process.Kill()
		_ = parent.Wait()
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("read child PID: %v", err)
	}
	childPID, err := strconv.ParseUint(strings.TrimSpace(line), 10, 32)
	if err != nil {
		t.Fatalf("parse child PID %q: %v", line, err)
	}
	const synchronize = 0x00100000
	child, err := syscall.OpenProcess(synchronize, false, uint32(childPID))
	if err != nil {
		t.Fatalf("open child: %v", err)
	}
	defer syscall.CloseHandle(child)

	if err := killDescendants(uint32(parent.Process.Pid)); err != nil {
		t.Fatalf("killDescendants: %v", err)
	}
	if event, err := syscall.WaitForSingleObject(child, 10_000); err != nil || event != syscall.WAIT_OBJECT_0 {
		t.Fatalf("child still running after killDescendants: event %d, err %v", event, err)
	}
	// killDescendants leaves the root to cmd.Process.Kill.
	root, err := syscall.OpenProcess(synchronize, false, uint32(parent.Process.Pid))
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer syscall.CloseHandle(root)
	if event, err := syscall.WaitForSingleObject(root, 0); err != nil || event != syscall.WAIT_TIMEOUT {
		t.Fatalf("root process was terminated by killDescendants: event %d, err %v", event, err)
	}
}
