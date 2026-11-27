package app

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestResolveVersionInfo(t *testing.T) {
	bi := &debug.BuildInfo{
		GoVersion: "go1.23.5",
		Main:      debug.Module{Version: "v0.1.0"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "1234567890abcdef"},
			{Key: "vcs.time", Value: "2026-11-28T01:15:12Z"},
			{Key: "vcs.modified", Value: "true"},
		},
	}

	got := ResolveVersionInfo(bi, "dev", "", "")
	if got.Version != "v0.1.0" || got.Commit != "1234567890abcdef" || !got.Dirty || got.Date != "2026-11-28T01:15:12Z" || got.GoVersion != "go1.23.5" {
		t.Fatalf("unexpected buildinfo resolution: %+v", got)
	}
	text := got.FormatText()
	if !strings.Contains(text, "reifu v0.1.0 (1234567-dirty, 2026-11-28T01:15:12Z, go1.23.5 ") {
		t.Fatalf("unexpected text format: %q", text)
	}

	// Explicit ldflags override buildinfo.
	override := ResolveVersionInfo(bi, "v1.2.3", "abcdef123456", "2026-12-01T00:00:00Z")
	if override.Version != "v1.2.3" || override.Commit != "abcdef123456" || override.Date != "2026-12-01T00:00:00Z" {
		t.Fatalf("expected ldflags override, got %+v", override)
	}

	// Missing buildinfo falls back to dev / unknown.
	fallback := ResolveVersionInfo(nil, "", "", "")
	if fallback.Version != "dev" || fallback.Commit != "unknown" || fallback.Dirty {
		t.Fatalf("unexpected fallback: %+v", fallback)
	}
	if !strings.Contains(fallback.FormatText(), "reifu dev (unknown, ") {
		t.Fatalf("unexpected fallback format: %q", fallback.FormatText())
	}
}
