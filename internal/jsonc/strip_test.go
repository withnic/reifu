package jsonc

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestStrip(t *testing.T) {
	input := []byte("{\n// header\n\"value\": [\"https://host/*x*/\", \"\\\"//\", /* block\ncomment */],\n}")
	clean, err := Strip(input)
	if err != nil || len(clean) != len(input) || bytes.Count(clean, []byte("\n")) != bytes.Count(input, []byte("\n")) || !json.Valid(clean) {
		t.Fatalf("strip: %q (%v)", clean, err)
	}
	var parsed map[string][]string
	if err := Unmarshal(input, &parsed); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(parsed["value"], []string{"https://host/*x*/", "\"//"}) {
		t.Fatalf("decoded %+v", parsed)
	}
	bomInput := append([]byte{0xef, 0xbb, 0xbf}, input...)
	var bomParsed map[string][]string
	if err := Unmarshal(bomInput, &bomParsed); err != nil || !reflect.DeepEqual(bomParsed["value"], parsed["value"]) {
		t.Fatalf("BOM unmarshal: %+v (%v)", bomParsed, err)
	}
}

func TestSyntaxErrors(t *testing.T) {
	for _, input := range []string{"[\n/* missing", "[\n1,,]", "[\n,]", "{\n\"a\":}", "[\n1/* no concatenation */2]", "[\n"} {
		var dst any
		if err := Unmarshal([]byte(input), &dst); err == nil || !strings.Contains(err.Error(), "line 2") {
			t.Errorf("input %q: got %v, want line 2 error", input, err)
		}
	}
}

func TestAppendAllowCommands(t *testing.T) {
	cases := []struct {
		array, want string
		existing    []string
	}{
		{"[]", "[\"new\", \"a\\\"\\\\b\"]", nil},
		{"[\"old\"]", "[\"old\", \"new\", \"a\\\"\\\\b\"]", []string{"old"}},
		{"[\"old\", /* note */]", "[\"old\", /* note */ \"new\", \"a\\\"\\\\b\",]", []string{"old"}},
		{"[/* note */]", "[/* note */ \"new\", \"a\\\"\\\\b\"]", nil},
		{"[\n\t\"old\" // inline\n  ]", "[\n\t\"old\", // inline\n\t\"new\",\n\t\"a\\\"\\\\b\"\n  ]", []string{"old"}},
		{"[\n  \"old\", // inline trailing\n]", "[\n  \"old\", // inline trailing\n  \"new\",\n  \"a\\\"\\\\b\",\n]", []string{"old"}},
		{"[\n  // empty\n]", "[\n  // empty\n  \"new\",\n  \"a\\\"\\\\b\"\n]", nil},
	}
	for _, tc := range cases {
		t.Run(tc.array, func(t *testing.T) {
			prefix := "{ /* header */ \"other\":{\"allow_commands\":[\"untouched\"]},\"permissions\":{\"allow_commands\":"
			suffix := "},\"tail\":true,}"
			commands := []string{"new", "a\"\\b"}
			out, err := AppendAllowCommands([]byte(prefix+tc.array+suffix), commands)
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != prefix+tc.want+suffix {
				t.Fatalf("got %s\nwant %s", out, prefix+tc.want+suffix)
			}
			var cfg struct {
				Permissions struct {
					Allow []string `json:"allow_commands"`
				}
			}
			if err := Unmarshal(out, &cfg); err != nil || !reflect.DeepEqual(cfg.Permissions.Allow, append(tc.existing, commands...)) {
				t.Fatalf("decoded %+v (%v)", cfg, err)
			}
		})
	}
}

func TestAppendFallbackAndInvalidTarget(t *testing.T) {
	for _, input := range []string{`{"unmanaged": 42}`, `{"permissions":{"deny_paths":["keep"]}, "unmanaged":42}`} {
		out, err := AppendAllowCommands([]byte(input), []string{"new"})
		if err != nil || !json.Valid(out) || !bytes.Contains(out, []byte(`"unmanaged": 42`)) || !bytes.Contains(out, []byte(`"new"`)) {
			t.Fatalf("fallback: %s (%v)", out, err)
		}
		if strings.Contains(input, "keep") && !bytes.Contains(out, []byte(`"keep"`)) {
			t.Fatal("fallback lost a sibling field")
		}
	}
	for _, input := range []string{`null`, `{"permissions":null}`, `{"permissions":{"allow_commands":null}}`, `{"permissions":{"allow_commands":[1]}}`} {
		if _, err := AppendAllowCommands([]byte(input), []string{"new"}); err == nil {
			t.Errorf("accepted %s", input)
		}
	}
	noopSrc := []byte("\xef\xbb\xbf{\"permissions\":{\"allow_commands\":[\"old\"]}}")
	if out, err := AppendAllowCommands(noopSrc, nil); err != nil || !bytes.Equal(out, noopSrc) {
		t.Fatalf("empty append: %q (%v)", out, err)
	}
}
