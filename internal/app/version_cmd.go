package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

// Version, Commit, and Date can be overridden at build time via -ldflags.
// When unset, values are automatically populated from Go's embedded build info.
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

type VersionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Dirty     bool   `json:"dirty"`
	Date      string `json:"date,omitempty"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

func CurrentVersionInfo() VersionInfo {
	info, _ := debug.ReadBuildInfo()
	return ResolveVersionInfo(info, Version, Commit, Date)
}

func ResolveVersionInfo(info *debug.BuildInfo, version, commit, date string) VersionInfo {
	v := strings.TrimSpace(version)
	if v == "" || v == "dev" {
		if info != nil && info.Main.Version != "" && info.Main.Version != "(devel)" {
			v = info.Main.Version
		} else {
			v = "dev"
		}
	}

	c := strings.TrimSpace(commit)
	d := strings.TrimSpace(date)
	dirty := false
	goVer := runtime.Version()
	if info != nil {
		if info.GoVersion != "" {
			goVer = info.GoVersion
		}
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				if c == "" {
					c = strings.TrimSpace(s.Value)
				}
			case "vcs.time":
				if d == "" {
					d = strings.TrimSpace(s.Value)
				}
			case "vcs.modified":
				if s.Value == "true" {
					dirty = true
				}
			}
		}
	}
	if c == "" {
		c = "unknown"
	}

	return VersionInfo{
		Version:   v,
		Commit:    c,
		Dirty:     dirty,
		Date:      d,
		GoVersion: goVer,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
}

func (v VersionInfo) FormatText() string {
	commit := v.Commit
	if len(commit) > 7 {
		commit = commit[:7]
	}
	if v.Dirty && commit != "unknown" {
		commit += "-dirty"
	}
	parts := []string{commit}
	if v.Date != "" {
		parts = append(parts, v.Date)
	}
	parts = append(parts, fmt.Sprintf("%s %s/%s", v.GoVersion, v.OS, v.Arch))
	return fmt.Sprintf("reifu %s (%s)", v.Version, strings.Join(parts, ", "))
}

func (c CLI) version(args []string) error {
	f := c.flags("version", "")
	asJSON := f.Bool("json", false, "output version information as JSON")
	operands, err := c.parseFlags(f, "version", "", args)
	if err != nil {
		return err
	}
	if len(operands) > 0 {
		return errors.New("version command takes no positional arguments")
	}

	info := CurrentVersionInfo()
	if *asJSON {
		enc := json.NewEncoder(c.Stdout)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		return enc.Encode(info)
	}
	_, err = fmt.Fprintln(c.Stdout, info.FormatText())
	return err
}
