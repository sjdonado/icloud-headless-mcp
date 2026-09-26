package mcpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

// CheckOutput validates a tool's payload against its declared output
// schema, as a structured-content client would see it (after a JSON round
// trip). It covers the subset ToolOutputs uses: type (one or a list),
// properties, items and additionalProperties. Tests in each package call
// it on their fixture payloads, so a schema cannot drift from what the
// tool returns.
func CheckOutput(tool string, payload any) error {
	s, ok := ToolOutputs[tool]
	if !ok {
		return fmt.Errorf("%s declares no output schema", tool)
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return err
	}
	return checkValue(tool, s, v)
}

func checkValue(path string, s schema, v any) error {
	if t, ok := s["type"]; ok {
		var types []string
		switch t := t.(type) {
		case string:
			types = []string{t}
		case []string:
			types = t
		}
		matched := false
		for _, want := range types {
			if typeMatches(want, v) {
				matched = true
			}
		}
		if !matched {
			return fmt.Errorf("%s: %T %v is not %v", path, v, v, types)
		}
	}
	switch v := v.(type) {
	case map[string]any:
		props, _ := s["properties"].(schema)
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if ps, ok := props[k].(schema); ok {
				if err := checkValue(path+"."+k, ps, v[k]); err != nil {
					return err
				}
			} else if ap, ok := s["additionalProperties"].(schema); ok {
				if err := checkValue(path+"."+k, ap, v[k]); err != nil {
					return err
				}
			}
		}
	case []any:
		if items, ok := s["items"].(schema); ok {
			for i, item := range v {
				if err := checkValue(fmt.Sprintf("%s[%d]", path, i), items, item); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func typeMatches(want string, v any) bool {
	switch want {
	case "null":
		return v == nil
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "number":
		_, ok := v.(json.Number)
		return ok
	case "integer":
		n, ok := v.(json.Number)
		if !ok {
			return false
		}
		f, err := n.Float64()
		return err == nil && f == math.Trunc(f)
	}
	return false
}
