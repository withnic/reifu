package app

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/withnic/reifu/internal/fsx"
)

func (c CLI) preset(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: reifu preset <list|export>")
	}
	switch args[0] {
	case "list":
		f := c.flags("preset list", "")
		operands, err := c.parseFlags(f, "preset list", "", args[1:])
		if err != nil {
			return err
		}
		if len(operands) != 0 {
			return errors.New("preset list takes no operands")
		}
		list, err := c.Presets.List()
		if err != nil {
			return err
		}
		for _, preset := range list {
			if _, err := fmt.Fprintf(c.Stdout, "%s\t%s\n", preset.Name, preset.Source); err != nil {
				return err
			}
		}
		return nil
	case "export":
		f := c.flags("preset export", "<name>")
		out := f.String("out", "", "destination directory")
		force := f.Bool("force", false, "replace an existing preset")
		operands, err := c.parseFlags(f, "preset export", "<name>", args[1:])
		if err != nil {
			return err
		}
		if len(operands) != 1 {
			return errors.New("preset export requires one preset name")
		}
		name := operands[0]
		raw, _, err := c.Presets.Load(name)
		if err != nil {
			return err
		}
		dir := *out
		if dir == "" {
			dir, err = c.Presets.Directory()
		} else {
			dir, err = filepath.Abs(dir)
		}
		if err != nil {
			return err
		}
		if err := fsx.EnsureRoot(dir); err != nil {
			return err
		}
		write := fsx.WriteNewFileAtomic
		if *force {
			write = fsx.WriteFileAtomic
		}
		file := name + ".jsonc"
		if err := fsx.WithFileLock(dir, func() error { return write(dir, file, raw) }); err != nil {
			return err
		}
		_, err = fmt.Fprintln(c.Stdout, filepath.Join(dir, file))
		return err
	case "--help", "-h", "help":
		_, err := fmt.Fprintln(c.Stdout, "Usage: reifu preset <list|export>")
		return err
	default:
		return fmt.Errorf("unknown preset command %q", args[0])
	}
}
