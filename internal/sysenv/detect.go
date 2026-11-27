package sysenv

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type Env string

const (
	EnvWindows     Env = "windows"
	EnvWSL2LinuxFS Env = "wsl2-ext4"
	EnvWSL2DrvFS   Env = "wsl2-ntfs"
	EnvDarwin      Env = "darwin"
	EnvLinux       Env = "linux"
)

// TargetOS returns "windows" for native Windows and "unix" for WSL2/Linux/macOS.
func (e Env) TargetOS() string {
	if e == EnvWindows {
		return "windows"
	}
	return "unix"
}

func Detect(root string) (Env, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve environment path %q: %w", root, err)
	}
	resolved, err := ResolvePath(abs)
	if err != nil {
		return "", fmt.Errorf("resolve environment path %q: %w", abs, err)
	}
	distro := os.Getenv("WSL_DISTRO_NAME")
	var version []byte
	if runtime.GOOS == "linux" && distro == "" {
		version, err = os.ReadFile("/proc/version")
		if err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("read /proc/version: %w", err)
		}
	}
	return classify(runtime.GOOS, distro, string(version), resolved)
}

func classify(goos, distro, version, path string) (Env, error) {
	switch goos {
	case "windows":
		return EnvWindows, nil
	case "darwin":
		return EnvDarwin, nil
	case "linux":
		version = strings.ToLower(version)
		if distro == "" && !strings.Contains(version, "microsoft") && !strings.Contains(version, "wsl") {
			return EnvLinux, nil
		}
		if drive, ok := strings.CutPrefix(path, "/mnt/"); ok && len(drive) > 0 {
			letter := drive[0]
			if (letter >= 'a' && letter <= 'z' || letter >= 'A' && letter <= 'Z') && (len(drive) == 1 || drive[1] == '/') {
				return EnvWSL2DrvFS, nil
			}
		}
		return EnvWSL2LinuxFS, nil
	default:
		return "", fmt.Errorf("unsupported OS %q", goos)
	}
}
