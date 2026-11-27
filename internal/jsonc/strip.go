package jsonc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"
)

// Strip replaces comments and trailing commas with whitespace, retaining byte offsets.
func Strip(src []byte) ([]byte, error) {
	if !utf8.Valid(src) {
		return nil, fmt.Errorf("JSONC is not valid UTF-8")
	}
	clean := bytes.Clone(src)
	i := 0
	if bytes.HasPrefix(clean, []byte{0xef, 0xbb, 0xbf}) {
		clean[0], clean[1], clean[2] = ' ', ' ', ' '
		i = 3
	}
	var previous byte
	comma := -1
	for i < len(src) {
		start := i
		switch {
		case src[i] == '"':
			i++
			for i < len(src) {
				ch := src[i]
				i++
				if ch == '"' {
					break
				}
				if ch == '\\' && i < len(src) {
					i++
				}
			}
			previous = '"'
			comma = -1
		case bytes.HasPrefix(src[i:], []byte("//")):
			i += 2
			for i < len(src) && src[i] != '\n' && src[i] != '\r' {
				i++
			}
			blank(clean[start:i])
		case bytes.HasPrefix(src[i:], []byte("/*")):
			end := bytes.Index(src[i+2:], []byte("*/"))
			if end < 0 {
				return nil, at(src, i+1, errors.New("unterminated block comment"))
			}
			i += end + 4
			blank(clean[start:i])
		case space(src[i]):
			i++
		default:
			ch := src[i]
			if (ch == ']' || ch == '}') && comma >= 0 {
				clean[comma] = ' '
			}
			comma = -1
			if ch == ',' && previous != 0 && !bytes.ContainsRune([]byte("[{,:"), rune(previous)) {
				comma = i
			}
			previous = ch
			i++
		}
	}
	return clean, nil
}

func space(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r'
}

func blank(data []byte) {
	for i, ch := range data {
		if ch != '\n' && ch != '\r' {
			data[i] = ' '
		}
	}
}

func Unmarshal(src []byte, dst any) error {
	clean, err := Strip(src)
	if err != nil {
		return err
	}
	err = json.Unmarshal(clean, dst)
	var syntax *json.SyntaxError
	var mismatch *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syntax):
		offset := int(syntax.Offset)
		if syntax.Error() == "unexpected end of JSON input" {
			offset = len(src) + 1
		}
		return at(src, offset, err)
	case errors.As(err, &mismatch):
		return at(src, int(mismatch.Offset), err)
	default:
		return err
	}
}

func at(src []byte, offset int, err error) error {
	end := min(len(src), max(0, offset-1))
	prefix := src[:end]
	line := bytes.Count(prefix, []byte("\n")) + 1
	column := utf8.RuneCount(prefix[bytes.LastIndexByte(prefix, '\n')+1:]) + 1
	return fmt.Errorf("JSONC line %d, column %d: %w", line, column, err)
}
