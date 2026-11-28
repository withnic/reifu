package app

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/withnic/reifu/internal/fsx"
	"github.com/withnic/reifu/internal/model"
	"github.com/withnic/reifu/internal/sysenv"
	"github.com/withnic/reifu/internal/template"
)

type CLI struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	Presets        template.Presets
	// WorkDir overrides the current working directory when nonempty (used in tests).
	WorkDir string
	// HomeDir overrides os.UserHomeDir when nonempty (used in tests).
	HomeDir string
}

const usage = `Usage: reifu <command> [options]

Commands:
  new <dir>      Create a new project directory from a preset
  init [dir]     Initialize reifu in an existing directory
  link [dir]     Verify or rebuild AGENTS.md links (CLAUDE.md, GEMINI.md)
  sync [dir]     Compile .reifu.jsonc to agent configs and detect drift
  status [dir]   Show environment, link, and sync status (--json, --quiet)
  preset         List or export presets (list, export)
  hook           Run cross-platform agent hooks (verify)
  run [dir]      Execute and review implementation phases
  logs [dir]     Show phase run history, logs, and reviews
  version        Show version and build commit hash (--json)
  help [cmd]     Show help for reifu or a specific command

JSON output is also available for new, init, link, sync, and preset list.`

func jsonEncoder(w io.Writer) *json.Encoder {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	return encoder
}

func writeJSON(w io.Writer, v any) error {
	if err := jsonEncoder(w).Encode(v); err != nil {
		return fmt.Errorf("write JSON: %w", err)
	}
	return nil
}

func (c CLI) Run(args []string) model.ExitCode {
	err := c.run(args)
	if errors.Is(err, flag.ErrHelp) {
		return model.ExitOK
	}
	if err != nil && err.Error() != "" {
		fmt.Fprintln(c.Stderr, err)
	}
	return model.ExitCodeOf(err)
}

func (c CLI) run(args []string) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch args[0] {
	case "-h", "--help":
		_, err := fmt.Fprintln(c.Stdout, usage)
		return err
	case "-v", "--version", "version":
		return c.version(args[1:])
	case "help":
		return c.help(args[1:])
	case "new":
		return c.newProject(args[1:])
	case "init":
		return c.initProject(args[1:])
	case "link":
		return c.link(args[1:])
	case "sync":
		return c.sync(args[1:])
	case "status":
		return c.status(args[1:])
	case "preset":
		return c.preset(args[1:])
	case "hook":
		return c.hook(args[1:])
	case "run":
		return c.runPhases(args[1:])
	case "logs":
		return c.logs(args[1:])
	default:
		return fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
}

func (c CLI) help(args []string) error {
	if len(args) == 0 {
		_, err := fmt.Fprintln(c.Stdout, usage)
		return err
	}
	switch args[0] {
	case "new", "init", "link", "sync", "status", "version", "run", "logs":
		return c.run([]string{args[0], "--help"})
	case "preset", "hook":
		if len(args) >= 2 {
			return c.run([]string{args[0], args[1], "--help"})
		}
		return c.run([]string{args[0], "--help"})
	default:
		return fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
}

func (c CLI) flags(command, operands string) *flag.FlagSet {
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	// Suppress automatic usage writes on parse errors so stdout stays clean (especially for --json).
	f.Usage = func() {}
	return f
}

func (c CLI) printUsage(f *flag.FlagSet, command, operands string) {
	if operands != "" {
		fmt.Fprintf(c.Stdout, "Usage: reifu %s [options] %s\n", command, operands)
	} else {
		fmt.Fprintf(c.Stdout, "Usage: reifu %s [options]\n", command)
	}
	f.SetOutput(c.Stdout)
	f.PrintDefaults()
	f.SetOutput(io.Discard)
}

// parseFlags accepts options on either side of positional operands.
func (c CLI) parseFlags(f *flag.FlagSet, command, operandsUsage string, args []string) ([]string, error) {
	var flags, operands []string
	for len(args) > 0 {
		arg := args[0]
		args = args[1:]
		if arg == "--" {
			operands = append(operands, args...)
			break
		}
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			operands = append(operands, arg)
			continue
		}
		flags = append(flags, arg)
		name, _, inline := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-"), "=")
		if opt := f.Lookup(name); opt != nil && !inline {
			if b, ok := opt.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
				continue
			}
			if len(args) > 0 {
				flags = append(flags, args[0])
				args = args[1:]
			}
		}
	}
	if err := f.Parse(flags); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			c.printUsage(f, command, operandsUsage)
		}
		return nil, err
	}
	return operands, nil
}

func (c CLI) resolveDir(dir string) (string, error) {
	if dir == "" {
		if c.WorkDir != "" {
			return filepath.Abs(c.WorkDir)
		}
		return filepath.Abs(".")
	}
	if !filepath.IsAbs(dir) && c.WorkDir != "" {
		return filepath.Abs(filepath.Join(c.WorkDir, dir))
	}
	return filepath.Abs(dir)
}

func (c CLI) projectRoot(f *flag.FlagSet, command string, args []string) (string, error) {
	operands, err := c.parseFlags(f, command, "[dir]", args)
	if err != nil {
		return "", err
	}
	switch len(operands) {
	case 0:
		return c.resolveDir("")
	case 1:
		return c.resolveDir(operands[0])
	default:
		return "", errors.New("expected at most one project directory")
	}
}

// rejectHomeDir blocks operating directly on the user's home directory so global
// agent directories (~/.claude, ~/.codex, ~/.gemini) can never be touched.
func (c CLI) rejectHomeDir(root string) error {
	home := c.HomeDir
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil || home == "" {
			return nil
		}
	}
	homeAbs, err := filepath.Abs(home)
	if err != nil {
		return nil
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil
	}
	homeResolved := homeAbs
	if resolved, err := sysenv.ResolvePath(homeAbs); err == nil {
		homeResolved = resolved
	}
	rootResolved := rootAbs
	if resolved, err := sysenv.ResolvePath(rootAbs); err == nil {
		rootResolved = resolved
	}
	if sameDirPath(homeAbs, rootAbs) || sameDirPath(homeResolved, rootResolved) {
		return fmt.Errorf("refusing to operate directly in user home directory %q; global agent settings are out of scope", root)
	}
	return nil
}

func sameDirPath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if a == b {
		return true
	}
	if os.PathSeparator == '\\' {
		return strings.EqualFold(a, b)
	}
	return false
}

func readRegular(root, target string) ([]byte, error) {
	if err := fsx.ValidateWithinRoot(root, target); err != nil {
		return nil, err
	}
	file := filepath.Join(root, filepath.FromSlash(target))
	info, err := os.Lstat(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", file, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", file)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", file, err)
	}
	return data, nil
}
