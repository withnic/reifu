package app

import (
	"fmt"

	"github.com/withnic/reifu/internal/syncer"
	"github.com/withnic/reifu/internal/sysenv"
)

func (c CLI) sync(args []string) error {
	f := c.flags("sync", "[dir]")
	var options syncer.Options
	f.BoolVar(&options.Import, "import", false, "import external allow rules")
	f.BoolVar(&options.Force, "force", false, "proceed despite drift or import guard")
	f.BoolVar(&options.Create, "create", false, "create missing agent directories")
	f.BoolVar(&options.NoHooks, "no-hooks", false, "suppress managed hooks")
	f.BoolVar(&options.DryRun, "dry-run", false, "preview changes without writing")
	root, err := c.projectRoot(f, "sync", args)
	if err != nil {
		return err
	}
	if err := c.rejectHomeDir(root); err != nil {
		return err
	}
	env, err := sysenv.Detect(root)
	if err != nil {
		return err
	}
	options.TargetOS, options.Warnings = string(env), c.Stderr
	result, err := syncer.Sync(root, options)
	if err != nil {
		return err
	}
	action := "Updated"
	if options.DryRun {
		action = "Would update"
	}
	for _, path := range result.ChangedPaths {
		if _, err := fmt.Fprintln(c.Stdout, action, path); err != nil {
			return err
		}
	}
	return nil
}
