package app

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/withnic/reifu/internal/fsx"
	"github.com/withnic/reifu/internal/model"
	"github.com/withnic/reifu/internal/syncer"
	"github.com/withnic/reifu/internal/sysenv"
	"github.com/withnic/reifu/internal/template"
)

func (c CLI) newProject(args []string) error {
	return c.setupProject("new", args, true)
}

func (c CLI) initProject(args []string) error {
	return c.setupProject("init", args, false)
}

func (c CLI) setupProject(command string, args []string, requireNew bool) error {
	operandsUsage := "[dir]"
	if requireNew {
		operandsUsage = "<dir>"
	}
	f := c.flags(command, operandsUsage)
	name := f.String("name", "", "project name (defaults to directory name)")
	preset := f.String("preset", "minimal", "project preset")
	spec := f.String("spec", "", "import an existing specification file into docs/specs/000_source_spec.md")
	noDocs := f.Bool("no-docs", false, "omit design document templates")
	noSettings := f.Bool("no-settings", false, "omit agent settings and the state snapshot")
	noHooks := f.Bool("no-hooks", false, "omit generated verification hooks")
	mode := f.String("link-mode", "", "override preset link mode")
	agents := f.String("agents", "", "comma-separated agent targets")
	var force *bool
	if requireNew {
		falseVal := false
		force = &falseVal
	} else {
		force = f.Bool("force", false, "replace existing boilerplate")
	}
	dryRun := f.Bool("dry-run", false, "preview changes without writing")
	operands, err := c.parseFlags(f, command, operandsUsage, args)
	if err != nil {
		return err
	}
	var root string
	explicitDir := len(operands) == 1
	if requireNew {
		if len(operands) != 1 {
			return errors.New(`new requires a target directory (e.g. "reifu new my-app")`)
		}
		root, err = c.resolveDir(operands[0])
	} else {
		switch len(operands) {
		case 0:
			root, err = c.resolveDir("")
		case 1:
			root, err = c.resolveDir(operands[0])
		default:
			return errors.New("expected at most one project directory")
		}
	}
	if err != nil {
		return err
	}
	if err := c.rejectHomeDir(root); err != nil {
		return err
	}
	if requireNew {
		if _, err := os.Lstat(root); err == nil {
			return fmt.Errorf("directory %q already exists (use \"reifu init\" for an existing directory)", root)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect directory %q: %w", root, err)
		}
	} else if !explicitDir && !*force {
		if err := guardImplicitInitDir(root, *name); err != nil {
			return err
		}
	}
	_, cfg, err := c.Presets.Load(*preset)
	if err != nil {
		return err
	}
	cfg.Project.Name = *name
	if cfg.Project.Name == "" {
		cfg.Project.Name = filepath.Base(root)
	}
	if *mode != "" {
		cfg.Links.Mode = model.LinkMode(*mode)
	}
	agentsSet := false
	f.Visit(func(opt *flag.Flag) { agentsSet = agentsSet || opt.Name == "agents" })
	if agentsSet {
		cfg.Agents.Targets = nil
		for _, agent := range strings.Split(*agents, ",") {
			cfg.Agents.Targets = append(cfg.Agents.Targets, model.AgentName(strings.TrimSpace(agent)))
		}
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	var sourceSpec []byte
	if *spec != "" {
		if *noDocs {
			return errors.New("--spec cannot be combined with --no-docs")
		}
		specFile, err := c.resolveDir(*spec)
		if err != nil {
			return err
		}
		sourceSpec, err = readSpecFile(specFile)
		if err != nil {
			return err
		}
	}
	files, err := template.RenderWithOptions(cfg, template.RenderOptions{
		Docs:          !*noDocs,
		HasSourceSpec: *spec != "",
	})
	if err != nil {
		return err
	}
	if sourceSpec != nil {
		files["docs/specs/000_source_spec.md"] = sourceSpec
	}
	targets := slices.Sorted(maps.Keys(files))
	reserved := append(slices.Clone(protectedLinkPaths), targets...)
	if err := fsx.ValidateDistinctPaths(reserved, cfg.Links.Targets); err != nil {
		return err
	}

	existing, err := fsx.DirectoryAncestor(root)
	if err != nil {
		return err
	}
	env, err := sysenv.Detect(existing)
	if err != nil {
		return err
	}
	options := syncer.Options{Create: true, Force: *force, NoHooks: *noHooks, TargetOS: string(env), Warnings: c.Stderr}
	run := func() error {
		writes := make(map[string][]byte)
		replace := make(map[string]bool)
		if existing == root || !*dryRun {
			if err := fsx.ValidateLinkPaths(root, reserved, cfg.Links.Targets); err != nil {
				return err
			}
			for _, target := range targets {
				proposed := files[target]
				current, err := readRegular(root, target)
				if err != nil {
					return err
				}
				if current != nil {
					if target == ".gitignore" {
						proposed = mergeIgnore(current, proposed)
					}
					if bytes.Equal(current, proposed) {
						continue
					}
					if target != ".gitignore" && !*force {
						return fmt.Errorf("initialize %s: file exists (use --force)", target)
					}
					replace[target] = true
				}
				writes[target] = proposed
			}
			reports, err := inspectLinks(root, cfg.Links)
			if err != nil {
				return err
			}
			if err := preflightLinks(cfg.Links, reports, *force); err != nil {
				return err
			}
			if !*noSettings {
				preview := options
				preview.DryRun = true
				if !*dryRun {
					preview.Warnings = nil
				}
				result, err := syncer.SyncInit(root, preview, files[syncer.MasterPath])
				if err != nil {
					return err
				}
				if *dryRun {
					for _, target := range result.ChangedPaths {
						if _, err := fmt.Fprintln(c.Stdout, "Would update", target); err != nil {
							return err
						}
					}
				}
			}
		} else {
			writes = files
		}
		if *dryRun {
			for _, target := range slices.Sorted(maps.Keys(writes)) {
				if _, err := fmt.Fprintln(c.Stdout, "Would write", target); err != nil {
					return err
				}
			}
			for _, target := range cfg.Links.Targets {
				if _, err := fmt.Fprintln(c.Stdout, "Would link", target); err != nil {
					return err
				}
			}
			if !*noSettings && existing != root {
				_, err := fmt.Fprintln(c.Stdout, "Would generate agent settings and", syncer.StatePath)
				return err
			}
			return nil
		}
		for _, target := range slices.Sorted(maps.Keys(writes)) {
			if parent := path.Dir(target); parent != "." {
				if err := fsx.EnsureDirWithinRoot(root, parent); err != nil {
					return err
				}
			}
			write := fsx.WriteNewFileAtomic
			if replace[target] {
				write = fsx.WriteFileAtomic
			}
			if err := write(root, target, writes[target]); err != nil {
				return err
			}
		}
		if err := c.createLinks(root, cfg.Links, *force); err != nil {
			return err
		}
		if !*noSettings {
			if _, err := syncer.SyncInit(root, options, files[syncer.MasterPath]); err != nil {
				return err
			}
		}
		_, err := fmt.Fprintln(c.Stdout, "Initialized", root)
		return err
	}
	if *dryRun {
		return run()
	}
	if err := fsx.EnsureRoot(root); err != nil {
		return err
	}
	return fsx.WithFileLock(root, run)
}

func mergeIgnore(current, defaults []byte) []byte {
	existing := strings.ReplaceAll(strings.ReplaceAll(string(current), "\r\n", "\n"), "\r", "\n")
	seen := strings.Split(strings.TrimPrefix(existing, "\ufeff"), "\n")
	var additions []string
	for _, line := range strings.Split(string(defaults), "\n") {
		if line != "" && !slices.Contains(seen, line) {
			additions = append(additions, line)
		}
	}
	if len(additions) == 0 {
		return current
	}
	if existing != "" && !strings.HasSuffix(existing, "\n") {
		existing += "\n"
	}
	return []byte(existing + strings.Join(additions, "\n") + "\n")
}

func guardImplicitInitDir(root, name string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("read directory %q: %w", root, err)
	}
	if len(entries) == 0 {
		return nil
	}
	for _, entry := range entries {
		switch entry.Name() {
		case ".git", syncer.MasterPath:
			return nil
		}
	}
	hint := "reifu new <dir>"
	if name != "" {
		hint = fmt.Sprintf("reifu new %s", name)
	}
	return fmt.Errorf("refusing to initialize non-empty directory %q without .git or %s; create a new directory (e.g. %q) or use --force", root, syncer.MasterPath, hint)
}

const maxSpecBytes = 10 << 20 // 10 MiB

func readSpecFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("read spec %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("spec %q is not a regular file", path)
	}
	if info.Size() > maxSpecBytes {
		return nil, fmt.Errorf("spec %q exceeds maximum size of %d bytes", path, maxSpecBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read spec %q: %w", path, err)
	}
	return data, nil
}
