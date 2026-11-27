package template

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/withnic/reifu/internal/jsonc"
	"github.com/withnic/reifu/internal/model"
)

func TestPresetsLoadAndRenderBuiltins(t *testing.T) {
	presets := Presets{Dir: filepath.Join(t.TempDir(), "presets")}
	list, err := presets.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 5 {
		t.Fatalf("unexpected builtin count: %+v", list)
	}
	for _, name := range []string{"go-cli", "ts-web", "ts-electron", "rust-cli", "minimal"} {
		t.Run(name, func(t *testing.T) {
			_, cfg, err := presets.Load(name)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Project.Name = `App "quoted" {{ .Raw }}`
			files, err := Render(cfg, true)
			if err != nil {
				t.Fatal(err)
			}
			for _, required := range []string{
				"AGENTS.md", ".gitignore", ".reifu.jsonc",
				"docs/design/01_project_overview.md", "docs/design/02_data_modeling.md",
				"docs/design/03_architecture.md", "docs/specs/README.md",
				"prompts/README.md", "prompts/phase0.md", "prompts/add_feature.md", "prompts/review.md",
			} {
				if len(files[required]) == 0 {
					t.Errorf("missing rendered file %s", required)
				}
			}
			if _, ok := files["prompts/phases/.gitkeep"]; !ok {
				t.Error("missing prompts/phases/.gitkeep")
			}
			if !bytes.Contains(files["prompts/phase0.md"], []byte("{{ .Name }}")) {
				t.Fatal("{{ }} delimiter content was altered")
			}
			if !bytes.Contains(files["AGENTS.md"], []byte("## Development commands")) {
				t.Fatalf("AGENTS.md missing development commands:\n%s", files["AGENTS.md"])
			}
			for _, ignored := range []string{".claude/settings.local.json", ".gemini/settings.local.json"} {
				if !bytes.Contains(files[".gitignore"], []byte(ignored)) {
					t.Errorf(".gitignore missing %s:\n%s", ignored, files[".gitignore"])
				}
			}
			// Templates must only mention the selected preset's stack, never unrelated stacks.
			joinedDocs := string(files["AGENTS.md"]) + "\n" + string(files["prompts/phase0.md"]) + "\n" + string(files["prompts/add_feature.md"]) + "\n" + string(files["docs/design/03_architecture.md"])
			switch name {
			case "go-cli":
				for _, unwanted := range []string{"npm init", "package.json", "TypeScript", "ESLint", "Biome"} {
					if strings.Contains(joinedDocs, unwanted) {
						t.Errorf("go-cli docs leaked unrelated stack keyword %q", unwanted)
					}
				}
			case "ts-web", "ts-electron":
				for _, unwanted := range []string{"go mod", "go.mod", "gofmt", "go vet", "Cargo.toml", "cargo "} {
					if strings.Contains(joinedDocs, unwanted) {
						t.Errorf("%s docs leaked unrelated stack keyword %q", name, unwanted)
					}
				}
			case "rust-cli":
				for _, unwanted := range []string{"npm init", "package.json", "TypeScript", "go mod", "go.mod", "gofmt"} {
					if strings.Contains(joinedDocs, unwanted) {
						t.Errorf("rust-cli docs leaked unrelated stack keyword %q", unwanted)
					}
				}
			}
			if bytes.Contains(files[".reifu.jsonc"], []byte(`\u0026`)) || bytes.Contains(files[".reifu.jsonc"], []byte(`\u003e`)) {
				t.Fatalf("rendered .reifu.jsonc contains HTML escapes:\n%s", files[".reifu.jsonc"])
			}
			var parsed model.ReifuConfig
			if err := jsonc.Unmarshal(files[".reifu.jsonc"], &parsed); err != nil {
				t.Fatalf("rendered .reifu.jsonc invalid: %v\n%s", err, files[".reifu.jsonc"])
			}
			if err := parsed.Validate(); err != nil {
				t.Fatalf("rendered .reifu.jsonc failed validation: %v", err)
			}
			if parsed.Project.Name != cfg.Project.Name {
				t.Fatalf("project name mismatch: got %q, want %q", parsed.Project.Name, cfg.Project.Name)
			}
			noDocs, err := Render(cfg, false)
			if err != nil {
				t.Fatal(err)
			}
			if len(noDocs) != 3 {
				t.Fatalf("expected 3 files with docs=false, got %d", len(noDocs))
			}
		})
	}
}

func TestPresetsOverrideAndSecurity(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "presets")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	presets := Presets{Dir: dir}

	for _, bad := range []string{"", "../go-cli", `..\go-cli`, "a/b", "bad.name", "_invalid"} {
		if _, _, err := presets.Load(bad); err == nil {
			t.Errorf("accepted unsafe preset name %q", bad)
		}
	}

	// Custom preset (e.g. C++ CMake) works purely via JSONC without any Go code changes.
	custom := `{"version":1,"project":{"preset":"cpp-cmake"},"permissions":{"allow_commands":["ctest *"]},"workflow":{"require_files":["CMakeLists.txt"],"commands":[{"step":"Test","run":"ctest --output-on-failure"}],"verify_steps":[["ctest","--output-on-failure"]],"gitignore":["build/"]}}`
	if err := os.WriteFile(filepath.Join(dir, "cpp-cmake.jsonc"), []byte(custom), 0644); err != nil {
		t.Fatal(err)
	}
	override := `{"version":1,"project":{"preset":"go-cli"},"permissions":{"allow_commands":["go test ./..."]}}`
	if err := os.WriteFile(filepath.Join(dir, "go-cli.jsonc"), []byte(override), 0644); err != nil {
		t.Fatal(err)
	}
	// Invalid filename in custom directory is ignored by List.
	if err := os.WriteFile(filepath.Join(dir, "_ignored.jsonc"), []byte(custom), 0644); err != nil {
		t.Fatal(err)
	}

	list, err := presets.List()
	if err != nil {
		t.Fatal(err)
	}
	sources := make(map[string]string, len(list))
	for _, p := range list {
		sources[p.Name] = p.Source
	}
	if sources["cpp-cmake"] != "custom" || sources["go-cli"] != "custom (overrides builtin)" || sources["_ignored"] != "" {
		t.Fatalf("unexpected preset list: %+v", list)
	}
	_, cfg, err := presets.Load("cpp-cmake")
	if err != nil || len(cfg.Permissions.AllowCommands) != 1 || cfg.Permissions.AllowCommands[0] != "ctest *" {
		t.Fatalf("load custom preset: %+v (%v)", cfg, err)
	}
	cfg.Project.Name = "my-cpp"
	rendered, err := Render(cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(rendered["AGENTS.md"], []byte("ctest --output-on-failure")) || !bytes.Contains(rendered[".gitignore"], []byte("build/")) {
		t.Fatalf("custom workflow not rendered:\nAGENTS:\n%s\nGITIGNORE:\n%s", rendered["AGENTS.md"], rendered[".gitignore"])
	}

	// Invalid preset content is rejected.
	if err := os.WriteFile(filepath.Join(dir, "broken.jsonc"), []byte(`{"version":99}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := presets.Load("broken"); err == nil || !strings.Contains(err.Error(), "validate preset") {
		t.Fatalf("expected validation error for broken preset: %v", err)
	}
}

func TestRenderSourceSpecMode(t *testing.T) {
	presets := Presets{Dir: filepath.Join(t.TempDir(), "presets")}
	_, cfg, err := presets.Load("go-cli")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Project.Name = "spec-app"
	withoutSpec, err := RenderWithOptions(cfg, RenderOptions{Docs: true, HasSourceSpec: false})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(withoutSpec["prompts/phase0.md"], []byte("00_source_spec.md")) {
		t.Fatal("phase0 should not mention 00_source_spec.md when HasSourceSpec is false")
	}
	withSpec, err := RenderWithOptions(cfg, RenderOptions{Docs: true, HasSourceSpec: true})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(withSpec["prompts/phase0.md"], []byte("docs/specs/000_source_spec.md")) ||
		!bytes.Contains(withSpec["prompts/phase0.md"], []byte("prompts/phases/")) {
		t.Fatalf("phase0 missing source spec or prompts/phases:\n%s", withSpec["prompts/phase0.md"])
	}
}
