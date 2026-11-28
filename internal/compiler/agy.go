package compiler

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/withnic/reifu/internal/model"
)

type agySettingsDoc struct {
	Permissions agyPermissions `json:"permissions"`
}

type agyPermissions struct {
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`
}

// CompileAgySettings converts .reifu.jsonc permissions into a session settings
// document for `agy --settings`. It returns (nil, false, nil) when no rules
// need to be passed.
func CompileAgySettings(cfg model.ReifuConfig) ([]byte, bool, error) {
	allow := compileAgyCommandRules(cfg.Permissions.AllowCommands, false)
	deny := compileAgyCommandRules(cfg.Permissions.DenyCommands, true)
	deny = appendUnique(deny, compileAgyDenyPathRules(cfg.Permissions.DenyPaths)...)
	if len(allow) == 0 && len(deny) == 0 {
		return nil, false, nil
	}
	doc := agySettingsDoc{
		Permissions: agyPermissions{
			Allow: allow,
			Deny:  deny,
		},
	}
	out, err := marshalJSON(doc)
	if err != nil {
		return nil, false, fmt.Errorf("encode agy settings: %w", err)
	}
	return out, true, nil
}

// compileAgyCommandRules translates reifu glob command patterns into agy
// command(...) rules that work on both POSIX shells (word-prefix matching)
// and Windows PowerShell/cmd (exact or full-line regex: matching).
func compileAgyCommandRules(patterns []string, isDeny bool) []string {
	var rules []string
	for _, raw := range patterns {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		if p == "*" {
			rules = appendUnique(rules, "command(*)")
			continue
		}
		if !strings.ContainsAny(p, "*?") {
			rules = appendUnique(rules, "command("+p+")")
			if isDeny {
				// On Windows, literal command(...) matches the full line exactly;
				// also deny argument forms so bare deny entries stay effective.
				rules = appendUnique(rules, "command(regex:"+regexp.QuoteMeta(p)+" .*)")
			}
			continue
		}
		if prefix, ok := strings.CutSuffix(p, " *"); ok {
			if prefix == "" {
				rules = appendUnique(rules, "command(*)")
				continue
			}
			if !strings.ContainsAny(prefix, "*?") {
				rules = appendUnique(rules, "command("+prefix+")")
			} else {
				rules = appendUnique(rules, "command(regex:"+globToAgyRegex(prefix)+")")
			}
			rules = appendUnique(rules, "command(regex:"+globToAgyRegex(p)+")")
			continue
		}
		rules = appendUnique(rules, "command(regex:"+globToAgyRegex(p)+")")
	}
	return rules
}

func globToAgyRegex(pattern string) string {
	escaped := regexp.QuoteMeta(pattern)
	return strings.NewReplacer(`\*`, `.*`, `\?`, `.`).Replace(escaped)
}

// compileAgyDenyPathRules translates literal deny_paths entries into agy
// read_file(<path>) deny rules (in agy, denying read_file also denies
// write_file). Glob patterns are skipped because agy's read_file matches
// literal path prefixes rather than globs.
func compileAgyDenyPathRules(paths []string) []string {
	var rules []string
	for _, raw := range paths {
		p := strings.TrimSpace(raw)
		if p == "" || strings.ContainsAny(p, "*?") {
			continue
		}
		p = strings.ReplaceAll(p, "\\", "/")
		for strings.HasPrefix(p, "./") {
			p = strings.TrimPrefix(p, "./")
		}
		if p == "" || p == "." {
			continue
		}
		rules = appendUnique(rules, "read_file("+p+")")
	}
	return rules
}

func appendUnique(dst []string, items ...string) []string {
	for _, item := range items {
		exists := false
		for _, prev := range dst {
			if prev == item {
				exists = true
				break
			}
		}
		if !exists {
			dst = append(dst, item)
		}
	}
	return dst
}
