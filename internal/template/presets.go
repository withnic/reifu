package template

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/withnic/reifu/internal/fsx"
	"github.com/withnic/reifu/internal/jsonc"
	"github.com/withnic/reifu/internal/model"
)

type Presets struct {
	// Dir overrides the default preset directory when nonempty.
	Dir string
}

type Preset struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

func (p Presets) Directory() (string, error) {
	if p.Dir != "" {
		return filepath.Abs(p.Dir)
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config directory: %w", err)
	}
	return filepath.Join(dir, "reifu", "presets"), nil
}

func (p Presets) List() ([]Preset, error) {
	entries, err := embedded.ReadDir("embedded/presets")
	if err != nil {
		return nil, err
	}
	sources := make(map[string]string)
	for _, entry := range entries {
		sources[strings.TrimSuffix(entry.Name(), ".jsonc")] = "builtin"
	}
	dir, err := p.Directory()
	if err != nil {
		return nil, err
	}
	entries, err = os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("list presets in %s: %w", dir, err)
	}
	for _, entry := range entries {
		name, ok := strings.CutSuffix(entry.Name(), ".jsonc")
		if !ok || !entry.Type().IsRegular() || model.ValidatePresetName(name) != nil {
			continue
		}
		source := "custom"
		if sources[name] == "builtin" {
			source = "custom (overrides builtin)"
		}
		sources[name] = source
	}
	list := make([]Preset, 0, len(sources))
	for name, source := range sources {
		list = append(list, Preset{name, source})
	}
	slices.SortFunc(list, func(a, b Preset) int { return strings.Compare(a.Name, b.Name) })
	return list, nil
}

func (p Presets) Load(name string) ([]byte, model.ReifuConfig, error) {
	var cfg model.ReifuConfig
	if err := model.ValidatePresetName(name); err != nil {
		return nil, cfg, err
	}
	dir, err := p.Directory()
	if err != nil {
		return nil, cfg, err
	}
	base := name + ".jsonc"
	file := filepath.Join(dir, base)
	var raw []byte
	info, err := os.Lstat(file)
	switch {
	case err == nil:
		if err := fsx.ValidateWithinRoot(dir, base); err != nil {
			return nil, cfg, err
		}
		if !info.Mode().IsRegular() {
			return nil, cfg, fmt.Errorf("preset %s is not a regular file", file)
		}
		raw, err = os.ReadFile(file)
	case errors.Is(err, os.ErrNotExist):
		file = "embedded/presets/" + base
		raw, err = embedded.ReadFile(file)
	}
	if err != nil {
		return nil, cfg, fmt.Errorf("read preset %s: %w", file, err)
	}
	if err := jsonc.Unmarshal(raw, &cfg); err != nil {
		return nil, cfg, fmt.Errorf("parse preset %s: %w", file, err)
	}
	cfg.Project.Preset = name
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, cfg, fmt.Errorf("validate preset %s: %w", file, err)
	}
	return raw, cfg, nil
}
