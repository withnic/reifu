package sysenv

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		os, distro, version, path string
		want                      Env
	}{
		{"windows", "", "", "C:/test", EnvWindows},
		{"darwin", "", "", "/tmp/test", EnvDarwin},
		{"linux", "", "native", "/mnt/c", EnvLinux},
		{"linux", "Ubuntu", "", "/home/user", EnvWSL2LinuxFS},
		{"linux", "", "Microsoft WSL2", "/mnt/C/project", EnvWSL2DrvFS},
		{"linux", "Ubuntu", "", "/mnt/z", EnvWSL2DrvFS},
		{"linux", "Ubuntu", "", "/mnt/cache", EnvWSL2LinuxFS},
		{"linux", "Ubuntu", "", "/mnt/copy/test", EnvWSL2LinuxFS},
	}
	for _, tc := range cases {
		got, err := classify(tc.os, tc.distro, tc.version, tc.path)
		if err != nil || got != tc.want {
			t.Errorf("%+v: got %q (%v)", tc, got, err)
		}
	}
	if _, err := classify("plan9", "", "", "/tmp"); err == nil {
		t.Fatal("unsupported OS accepted")
	}
	if EnvWindows.TargetOS() != "windows" || EnvWSL2LinuxFS.TargetOS() != "unix" || EnvWSL2DrvFS.TargetOS() != "unix" || EnvDarwin.TargetOS() != "unix" || EnvLinux.TargetOS() != "unix" {
		t.Fatal("unexpected TargetOS mapping")
	}
}

func TestDetect(t *testing.T) {
	root := t.TempDir()
	got, err := Detect(root)
	if err != nil {
		t.Fatal(err)
	}
	switch runtime.GOOS {
	case "windows":
		if got != EnvWindows {
			t.Fatal(got)
		}
	case "darwin":
		if got != EnvDarwin {
			t.Fatal(got)
		}
	case "linux":
		if got != EnvLinux && got != EnvWSL2LinuxFS && got != EnvWSL2DrvFS {
			t.Fatal(got)
		}
	}
	if _, err := Detect(filepath.Join(root, "missing")); err == nil {
		t.Fatal("missing path accepted")
	}
}
