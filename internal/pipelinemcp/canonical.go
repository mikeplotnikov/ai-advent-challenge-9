package pipelinemcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// CanonicalJSON is the cross-language hash contract for day 19. It recursively
// sorts object keys lexicographically, emits no insignificant whitespace, uses
// UTF-8, and disables HTML escaping. Dataset and summary numeric values are
// fixed-width strings defined in types.go, never JSON floating-point numbers.
func CanonicalJSON(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var generic any
	if err := decoder.Decode(&generic); err != nil {
		return "", err
	}
	var out strings.Builder
	if err := writeCanonical(&out, generic); err != nil {
		return "", err
	}
	return out.String(), nil
}

func writeCanonical(out *strings.Builder, value any) error {
	switch current := value.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		if current {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case json.Number:
		out.WriteString(current.String())
	case string:
		var encoded bytes.Buffer
		encoder := json.NewEncoder(&encoded)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(current); err != nil {
			return err
		}
		out.Write(bytes.TrimSuffix(encoded.Bytes(), []byte("\n")))
	case []any:
		out.WriteByte('[')
		for i, item := range current {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := writeCanonical(out, item); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(current))
		for key := range current {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := writeCanonical(out, key); err != nil {
				return err
			}
			out.WriteByte(':')
			if err := writeCanonical(out, current[key]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	default:
		return fmt.Errorf("неподдерживаемый тип канонического JSON %T", value)
	}
	return nil
}
