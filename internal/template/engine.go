package template

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
	"text/template"
	"time"

	"github.com/withnic/reifu/internal/model"
)

//go:embed all:embedded
var embedded embed.FS

type TemplateData struct {
	ProjectName, Preset string
	Year                int
	Config              model.ReifuConfig
	Commands            []model.CommandStep
	Gitignore           []string
	HasSourceSpec       bool
}

type RenderOptions struct {
	Docs          bool
	HasSourceSpec bool
}

func Render(cfg model.ReifuConfig, docs bool) (map[string][]byte, error) {
	return RenderWithOptions(cfg, RenderOptions{Docs: docs})
}

func RenderWithOptions(cfg model.ReifuConfig, opts RenderOptions) (map[string][]byte, error) {
	if cfg.Permissions.AllowCommands == nil {
		cfg.Permissions.AllowCommands = []string{}
	}
	if cfg.Permissions.DenyCommands == nil {
		cfg.Permissions.DenyCommands = []string{}
	}
	if cfg.Permissions.DenyPaths == nil {
		cfg.Permissions.DenyPaths = []string{}
	}
	data := TemplateData{
		ProjectName:   cfg.Project.Name,
		Preset:        cfg.Project.Preset,
		Year:          time.Now().Year(),
		Config:        cfg,
		Commands:      cfg.Workflow.Commands,
		Gitignore:     cfg.Workflow.Gitignore,
		HasSourceSpec: opts.HasSourceSpec,
	}
	files := map[string]string{
		"AGENTS.md":    "common/AGENTS.md.tmpl",
		".gitignore":   "common/gitignore.tmpl",
		".reifu.jsonc": "common/reifu.jsonc.tmpl",
	}
	if opts.Docs {
		files["docs/specs/README.md"] = "specs/README.md.tmpl"
		files["prompts/phases/.gitkeep"] = "prompts/phases/.gitkeep"
		docEntries, err := fs.ReadDir(embedded, "embedded/docs")
		if err != nil {
			return nil, err
		}
		for _, entry := range docEntries {
			if entry.IsDir() {
				continue
			}
			name := "docs/" + entry.Name()
			target := "docs/design/" + strings.TrimSuffix(entry.Name(), ".tmpl")
			files[target] = name
		}
		promptEntries, err := fs.ReadDir(embedded, "embedded/prompts")
		if err != nil {
			return nil, err
		}
		for _, entry := range promptEntries {
			if entry.IsDir() {
				continue
			}
			name := "prompts/" + entry.Name()
			target := "prompts/" + strings.TrimSuffix(entry.Name(), ".tmpl")
			files[target] = name
		}
	}
	result := make(map[string][]byte, len(files))
	for target, source := range files {
		raw, err := embedded.ReadFile("embedded/" + source)
		if err != nil {
			return nil, err
		}
		tmpl, err := template.New(source).Delims("[[", "]]").Option("missingkey=error").Funcs(template.FuncMap{
			"json": jsonText,
			"join": strings.Join,
		}).Parse(string(raw))
		if err != nil {
			return nil, fmt.Errorf("parse template %s: %w", source, err)
		}
		var out bytes.Buffer
		if err := tmpl.Execute(&out, data); err != nil {
			return nil, fmt.Errorf("render %s: %w", source, err)
		}
		result[target] = out.Bytes()
	}
	return result, nil
}

func jsonText(value any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}
