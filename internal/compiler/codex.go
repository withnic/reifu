package compiler

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/withnic/reifu/internal/model"
)

// CompileCodex updates the managed TOML scalars while preserving other lines.
func CompileCodex(existing []byte, cfg model.ReifuConfig) ([]byte, error) {
	// Validate only the fields used by this compiler.
	sandbox := model.ReifuConfig{
		Version: model.CurrentSchemaVersion,
		Project: model.ProjectMeta{Preset: "minimal"},
		Sandbox: cfg.Sandbox,
	}
	sandbox.ApplyDefaults()
	if err := sandbox.Validate(); err != nil {
		return nil, err
	}
	policy := strconv.Quote(string(sandbox.Sandbox.ApprovalPolicy))
	mode := strconv.Quote(string(sandbox.Sandbox.Mode))
	network := strconv.FormatBool(sandbox.Sandbox.NetworkAccess)

	source := strings.TrimPrefix(string(existing), "\ufeff")
	source = strings.ReplaceAll(strings.ReplaceAll(source, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(strings.TrimSuffix(source, "\n"), "\n")
	if source == "" {
		lines = nil
	}
	var scan tomlScan
	rootEnd, workspaceEnd := len(lines), -1
	var inTable, inWorkspace, workspaceExists, workspaceDotted bool
	seen := make(map[string]bool)
	for i, line := range lines {
		continuation := scan.quote != "" || scan.depth != 0
		comment, err := scan.line(line)
		if err != nil {
			return nil, fmt.Errorf("TOML line %d: %w", i+1, err)
		}
		if continuation {
			continue
		}
		body := strings.TrimSpace(line[:comment])
		if strings.HasPrefix(body, "[") {
			arrayTable := strings.HasPrefix(body, "[[")
			open, close := "[", "]"
			if arrayTable {
				open, close = "[[", "]]"
			}
			header, closed := strings.CutSuffix(strings.TrimPrefix(body, open), close)
			if !closed || scan.depth != 0 || scan.quote != "" || strings.TrimSpace(header) == "" {
				return nil, fmt.Errorf("invalid TOML table header on line %d", i+1)
			}
			if !inTable {
				rootEnd = i
			}
			if inWorkspace {
				workspaceEnd = i
			}
			inTable = true
			table := tomlKey(strings.TrimSpace(header))
			parent, remainder, nested := strings.Cut(header, ".")
			parent = tomlKey(strings.TrimSpace(parent))
			child, _, _ := strings.Cut(remainder, ".")
			child = tomlKey(strings.TrimSpace(child))
			if parent == "approval_policy" || parent == "sandbox_mode" ||
				nested && parent == "sandbox_workspace_write" && child == "network_access" {
				return nil, fmt.Errorf("managed scalar is defined as a table on line %d", i+1)
			}
			inWorkspace = table == "sandbox_workspace_write"
			if inWorkspace && arrayTable {
				return nil, fmt.Errorf("sandbox_workspace_write must be a table")
			}
			if inWorkspace {
				if workspaceExists || workspaceDotted {
					return nil, fmt.Errorf("duplicate sandbox_workspace_write table")
				}
				workspaceExists = true
				workspaceEnd = len(lines)
			}
			continue
		}

		left, _, ok := strings.Cut(body, "=")
		if !ok {
			continue
		}
		key := tomlKey(strings.TrimSpace(left))
		var value string
		switch {
		case !inTable && key == "approval_policy":
			value = policy
		case !inTable && key == "sandbox_mode":
			value = mode
		case inWorkspace && key == "network_access":
			value = network
		case !inTable:
			first, suffix, dotted := strings.Cut(left, ".")
			first = tomlKey(strings.TrimSpace(first))
			if key == "sandbox_workspace_write" || dotted && (first == "approval_policy" || first == "sandbox_mode") {
				return nil, fmt.Errorf("incompatible managed TOML definition on line %d", i+1)
			}
			if dotted && first == "sandbox_workspace_write" {
				workspaceDotted = true
				if tomlKey(strings.TrimSpace(suffix)) == "network_access" {
					key, value = "network_access", network
				} else if subkey, _, nested := strings.Cut(suffix, "."); nested && tomlKey(strings.TrimSpace(subkey)) == "network_access" {
					return nil, fmt.Errorf("network_access must be a boolean on line %d", i+1)
				}
			}
		case inWorkspace:
			first, _, dotted := strings.Cut(left, ".")
			if dotted && tomlKey(strings.TrimSpace(first)) == "network_access" {
				return nil, fmt.Errorf("network_access must be a boolean on line %d", i+1)
			}
		}
		if value == "" {
			continue
		}
		if seen[key] || scan.quote != "" || scan.depth != 0 {
			return nil, fmt.Errorf("duplicate or multiline managed key %q on line %d", key, i+1)
		}
		seen[key] = true
		equal := strings.IndexByte(line, '=')
		right := line[equal+1 : comment]
		trimmed := strings.TrimSpace(right)
		if trimmed == "" {
			return nil, fmt.Errorf("missing managed value for %q on line %d", key, i+1)
		}
		start := equal + 1 + strings.Index(right, trimmed)
		lines[i] = line[:start] + value + line[start+len(trimmed):]
	}
	if scan.quote != "" || scan.depth != 0 {
		return nil, fmt.Errorf("unterminated TOML value")
	}

	if workspaceExists && !seen["network_access"] {
		lines = insertLines(lines, workspaceEnd, []string{"network_access = " + network})
	}
	var root []string
	if !seen["approval_policy"] {
		root = append(root, "approval_policy = "+policy)
	}
	if !seen["sandbox_mode"] {
		root = append(root, "sandbox_mode = "+mode)
	}
	if workspaceDotted && !seen["network_access"] {
		root = append(root, "sandbox_workspace_write.network_access = "+network)
	}
	lines = insertLines(lines, rootEnd, root)
	if !workspaceExists && !workspaceDotted {
		lines = append(lines, "", "[sandbox_workspace_write]", "network_access = "+network)
	}
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}

func insertLines(lines []string, at int, inserted []string) []string {
	result := make([]string, 0, len(lines)+len(inserted))
	result = append(result, lines[:at]...)
	result = append(result, inserted...)
	return append(result, lines[at:]...)
}

func tomlKey(key string) string {
	if len(key) >= 2 && key[0] == '\'' && key[len(key)-1] == '\'' {
		return key[1 : len(key)-1]
	}
	if len(key) > 0 && key[0] == '"' {
		if unquoted, err := strconv.Unquote(key); err == nil {
			return unquoted
		}
	}
	return key
}

// tomlScan tracks strings and bracketed values across lines so text in
// arrays and multiline strings is never mistaken for a key or table.
type tomlScan struct {
	quote string
	depth int
}

func (s *tomlScan) line(line string) (int, error) {
	for i := 0; i < len(line); i++ {
		ch := line[i]
		if s.quote != "" {
			if ch == '\\' && s.quote[0] == '"' {
				i++
				continue
			}
			if strings.HasPrefix(line[i:], s.quote) {
				i += len(s.quote) - 1
				if len(s.quote) == 3 {
					for extra := 0; extra < 2 && i+1 < len(line) && line[i+1] == ch; extra++ {
						i++
					}
				}
				s.quote = ""
			}
			continue
		}
		switch ch {
		case '#':
			return i, nil
		case '"', '\'':
			s.quote = line[i : i+1]
			if strings.HasPrefix(line[i:], strings.Repeat(s.quote, 3)) {
				s.quote = line[i : i+3]
				i += 2
			}
		case '[', '{':
			s.depth++
		case ']', '}':
			s.depth--
			if s.depth < 0 {
				return 0, fmt.Errorf("unmatched closing bracket")
			}
		}
	}
	if len(s.quote) == 1 {
		return 0, fmt.Errorf("unterminated quoted string")
	}
	return len(line), nil
}
