package jsonc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// AppendAllowCommands appends to permissions.allow_commands while retaining
// existing text. If the field is absent it adds it via formatted JSON output.
func AppendAllowCommands(src []byte, commands []string) ([]byte, error) {
	var root map[string]json.RawMessage
	if err := Unmarshal(src, &root); err != nil {
		return nil, err
	}
	if root == nil {
		return nil, fmt.Errorf("expected a JSONC object")
	}
	if len(commands) == 0 {
		return bytes.Clone(src), nil
	}
	clean, err := Strip(src)
	if err != nil {
		return nil, err
	}
	key, permissions, start, err := objectField(clean, "permissions")
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	var array json.RawMessage
	var offset int
	if permissions != nil {
		if err := json.Unmarshal(permissions, &fields); err != nil || fields == nil {
			return nil, fmt.Errorf("permissions must be an object")
		}
		_, array, offset, err = objectField(permissions, "allow_commands")
		if err != nil {
			return nil, err
		}
	}
	if array == nil {
		if fields == nil {
			fields = make(map[string]json.RawMessage)
			key = "permissions"
		}
		fields["allow_commands"], err = marshalCompact(commands)
		if err != nil {
			return nil, err
		}
		root[key], err = marshalCompact(fields)
		if err != nil {
			return nil, err
		}
		out, err := marshalIndent(root)
		if err != nil {
			return nil, err
		}
		return verifyEdit(out)
	}
	var existing []string
	if array[0] != '[' {
		return nil, fmt.Errorf("permissions.allow_commands must be an array")
	}
	if err := json.Unmarshal(array, &existing); err != nil {
		return nil, fmt.Errorf("decode permissions.allow_commands: %w", err)
	}
	encoded := make([][]byte, len(commands))
	for i, command := range commands {
		encoded[i], err = marshalCompact(command)
		if err != nil {
			return nil, err
		}
	}

	open := start + offset
	close := open + len(array) - 1
	// The stripped copy hides an existing trailing comma. Inserting a comma
	// at the last value's end keeps any inline comment on its original line.
	last := open + len(bytes.TrimRightFunc(array[:len(array)-1], func(r rune) bool {
		return r <= 127 && space(byte(r))
	}))
	trailing, err := hasComma(src[last:close])
	if err != nil {
		return nil, err
	}

	var out bytes.Buffer
	out.Write(src[:last])
	if len(existing) != 0 && !trailing {
		out.WriteByte(',')
	}
	lineStart := bytes.LastIndexByte(src[:close], '\n') + 1
	if lineStart > open && len(bytes.TrimSpace(src[lineStart:close])) == 0 {
		indent := src[lineStart:close]
		entryIndent := append(bytes.Clone(indent), ' ', ' ')
		if len(existing) != 0 {
			token := open + 1
			for token < last && space(clean[token]) {
				token++
			}
			entryStart := bytes.LastIndexByte(src[:token], '\n') + 1
			if entryStart > open && len(bytes.TrimSpace(src[entryStart:token])) == 0 {
				entryIndent = src[entryStart:token]
			}
		}
		out.Write(src[last:lineStart])
		for i, value := range encoded {
			out.Write(entryIndent)
			out.Write(value)
			if i < len(encoded)-1 || trailing {
				out.WriteByte(',')
			}
			out.WriteByte('\n')
		}
		out.Write(indent)
	} else {
		out.Write(src[last:close])
		if close > open+1 && !space(src[close-1]) {
			out.WriteByte(' ')
		}
		out.Write(bytes.Join(encoded, []byte(", ")))
		if trailing {
			out.WriteByte(',')
		}
	}
	out.Write(src[close:])
	return verifyEdit(out.Bytes())
}

func hasComma(tail []byte) (bool, error) {
	clean, err := Strip(tail)
	return bytes.ContainsRune(clean, ','), err
}

// objectField follows encoding/json's case-insensitive struct field matching
// and selects the last matching member. Offsets refer to the stripped input.
func objectField(data []byte, name string) (key string, value json.RawMessage, offset int, err error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if _, err = dec.Token(); err != nil {
		return
	}
	for dec.More() {
		var token json.Token
		token, err = dec.Token()
		if err != nil {
			return
		}
		var raw json.RawMessage
		if err = dec.Decode(&raw); err != nil {
			return
		}
		field := token.(string)
		if strings.EqualFold(field, name) {
			key, value, offset = field, raw, int(dec.InputOffset())-len(raw)
		}
	}
	return
}

func verifyEdit(out []byte) ([]byte, error) {
	var check map[string]json.RawMessage
	if err := Unmarshal(out, &check); err != nil {
		return nil, fmt.Errorf("verify edited JSONC: %w", err)
	}
	return out, nil
}

func marshalCompact(value any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func marshalIndent(value any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
