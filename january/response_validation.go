package january

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Response validation checks required-field presence only. Request-only restrictions
// (enums, ranges, patterns, formats) must not reject forward-compatible responses.
// Normal typed decoding still handles incompatible JSON types afterwards.
type responsePresenceSchema struct {
	Ref        string                     `json:"$ref"`
	Required   []string                   `json:"required"`
	Properties map[string]json.RawMessage `json:"properties"`
	Items      json.RawMessage            `json:"items"`
	AllOf      []json.RawMessage          `json:"allOf"`
}

func validateResponseRequired(raw, rule json.RawMessage) error {
	if len(rule) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	return validateResponsePresence(value, rule, "response", 0)
}

func validateResponsePresence(value any, rule json.RawMessage, path string, depth int) error {
	if len(rule) == 0 {
		return nil
	}
	if depth > 64 {
		return fmt.Errorf("january: response schema nesting limit")
	}
	var s responsePresenceSchema
	if err := json.Unmarshal(rule, &s); err != nil {
		return fmt.Errorf("january: invalid generated response schema")
	}
	if s.Ref != "" {
		key := strings.TrimPrefix(s.Ref, "#/components/schemas/")
		ref, ok := generatedSchemas[key]
		if key == s.Ref || !ok {
			return fmt.Errorf("january: unresolved generated response schema")
		}
		if err := validateResponsePresence(value, ref, path, depth+1); err != nil {
			return err
		}
	}
	for _, child := range s.AllOf {
		if err := validateResponsePresence(value, child, path, depth+1); err != nil {
			return err
		}
	}
	switch v := value.(type) {
	case map[string]any:
		for _, key := range s.Required {
			if _, exists := v[key]; !exists {
				return fmt.Errorf("january: required response field missing: %s.%s", path, key)
			}
		}
		keys := make([]string, 0, len(s.Properties))
		for key := range s.Properties {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if item, exists := v[key]; exists {
				if err := validateResponsePresence(item, s.Properties[key], path+"."+key, depth+1); err != nil {
					return err
				}
			}
		}
	case []any:
		for i, item := range v {
			if err := validateResponsePresence(item, s.Items, fmt.Sprintf("%s[%d]", path, i), depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}
