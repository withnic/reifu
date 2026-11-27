package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/withnic/reifu/internal/fsx"
	"github.com/withnic/reifu/internal/jsonc"
	"github.com/withnic/reifu/internal/model"
	"github.com/withnic/reifu/internal/syncer"
)

type hookStdinPayload struct {
	StopHookActive bool `json:"stop_hook_active"`
}

func (c CLI) hook(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: reifu hook <verify> [dir]")
	}
	switch args[0] {
	case "verify":
		return c.hookVerify(args[1:])
	case "--help", "-h", "help":
		_, err := fmt.Fprintln(c.Stdout, "Usage: reifu hook <verify> [dir]")
		return err
	default:
		return fmt.Errorf("unknown hook command %q", args[0])
	}
}

// hookVerify executes workflow.verify_steps defined in .reifu.jsonc in a
// cross-platform way without shell syntax differences or stdout pollution.
// It exits 0 when stop_hook_active is true, BLOCKED_REPORT.md exists, or any
// require_files entry is not yet present; it exits 2 when a verification step fails.
func (c CLI) hookVerify(args []string) error {
	f := c.flags("hook verify", "[dir]")
	root, err := c.projectRoot(f, "hook verify", args)
	if err != nil {
		return err
	}
	if c.isStopHookActive() {
		return nil
	}
	if _, err := os.Stat(filepath.Join(root, "BLOCKED_REPORT.md")); err == nil {
		return nil
	}
	master, err := readRegular(root, syncer.MasterPath)
	if err != nil {
		return err
	}
	if master == nil {
		return nil
	}
	var cfg model.ReifuConfig
	if err := jsonc.Unmarshal(master, &cfg); err != nil {
		return fmt.Errorf("parse %s: %w", syncer.MasterPath, err)
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate %s: %w", syncer.MasterPath, err)
	}
	if len(cfg.Workflow.VerifySteps) == 0 {
		return nil
	}
	for _, req := range cfg.Workflow.RequireFiles {
		if err := fsx.ValidateWithinRoot(root, req); err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(req))); os.IsNotExist(err) {
			return nil
		}
	}
	pkgScripts := readPackageScripts(root)
	for _, step := range cfg.Workflow.VerifySteps {
		if shouldSkipNodeScript(step, pkgScripts) {
			continue
		}
		cmd := exec.Command(step[0], step[1:]...)
		cmd.Dir = root
		// Keep stdout completely clean for agent hook JSON protocols.
		cmd.Stdout = c.Stderr
		cmd.Stderr = c.Stderr
		if err := cmd.Run(); err != nil {
			return &model.ExitError{
				Code: model.ExitDrift,
				Err:  fmt.Errorf("verification step %q failed: %w", strings.Join(step, " "), err),
			}
		}
	}
	return nil
}

const hookStdinTimeout = 100 * time.Millisecond

func (c CLI) isStopHookActive() bool {
	if c.Stdin == nil {
		return false
	}
	if f, ok := c.Stdin.(*os.File); ok {
		fi, err := f.Stat()
		if err != nil || (fi.Mode()&os.ModeCharDevice) != 0 {
			return false
		}
	}
	ch := make(chan []byte, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(c.Stdin, 1<<20))
		if err == nil && len(data) > 0 {
			ch <- data
			return
		}
		ch <- nil
	}()
	var data []byte
	select {
	case data = <-ch:
	case <-time.After(hookStdinTimeout):
		return false
	}
	if len(data) == 0 {
		return false
	}
	var payload hookStdinPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return false
	}
	return payload.StopHookActive
}

func readPackageScripts(root string) map[string]string {
	raw, err := readRegular(root, "package.json")
	if err != nil || raw == nil {
		return nil
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return nil
	}
	return pkg.Scripts
}

func shouldSkipNodeScript(step []string, scripts map[string]string) bool {
	if scripts == nil || len(step) < 2 {
		return false
	}
	bin := strings.ToLower(strings.TrimSuffix(filepath.Base(step[0]), ".exe"))
	var scriptName string
	switch bin {
	case "npm", "pnpm", "bun":
		if step[1] == "run" && len(step) >= 3 {
			scriptName = step[2]
		} else if step[1] == "test" {
			scriptName = "test"
		}
	case "yarn":
		if step[1] == "run" && len(step) >= 3 {
			scriptName = step[2]
		} else {
			scriptName = step[1]
		}
	default:
		return false
	}
	if scriptName == "" {
		return false
	}
	body, ok := scripts[scriptName]
	if !ok {
		return true
	}
	if scriptName == "test" && strings.Contains(body, "no test specified") {
		return true
	}
	return false
}
