package january

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// All rules originate from the generated contract; validation never guesses wire names.
type validationSchema struct {
	Ref        string                     `json:"$ref"`
	Type       string                     `json:"type"`
	Nullable   bool                       `json:"nullable"`
	Required   []string                   `json:"required"`
	Properties map[string]json.RawMessage `json:"properties"`
	Items      json.RawMessage            `json:"items"`
	AllOf      []json.RawMessage          `json:"allOf"`
	Enum       []any                      `json:"enum"`
	Minimum    *float64                   `json:"minimum"`
	Maximum    *float64                   `json:"maximum"`
	// OpenAPI 3.0 exclusive bounds: true makes minimum/maximum exclusive.
	ExclusiveMinimum bool `json:"exclusiveMinimum"`
	ExclusiveMaximum bool `json:"exclusiveMaximum"`
	// RangeByUnit holds an object's value to the range of the unit it names,
	// for example 1–811.5 fl_oz but 30–24000 ml. A property holding a shared
	// object may narrow it further (a weight-log weight is 10–1000 lb).
	RangeByUnit *rangeByUnit `json:"x-january-range-by-unit"`
	MinLength   *int         `json:"minLength"`
	MaxLength   *int         `json:"maxLength"`
	MinItems    *int         `json:"minItems"`
	MaxItems    *int         `json:"maxItems"`
	Pattern     string       `json:"pattern"`
	Format      string       `json:"format"`
}

type rangeByUnit struct {
	ValueProperty string `json:"valueProperty"`
	UnitProperty  string `json:"unitProperty"`
	Ranges        map[string]struct {
		Minimum float64 `json:"minimum"`
		Maximum float64 `json:"maximum"`
	} `json:"ranges"`
}

// check applies the range of the object's unit to its value. An unknown or
// missing unit is left to the unit property's own rule.
func (r *rangeByUnit) check(object map[string]any, field string) error {
	value, ok := object[r.ValueProperty].(float64)
	unit, unitOK := object[r.UnitProperty].(string)
	if !ok || !unitOK {
		return nil
	}
	limits, known := r.Ranges[unit]
	if !known || (value >= limits.Minimum && value <= limits.Maximum) {
		return nil
	}
	format := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	return fmt.Errorf("%w: %s.%s must be from %s through %s %s", ErrInvalidInput, field, r.ValueProperty, format(limits.Minimum), format(limits.Maximum), unit)
}

func validateRaw(raw, rule json.RawMessage, field string) error {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("%w: invalid JSON for %s", ErrInvalidInput, field)
	}
	return validateValue(value, rule, field, 0)
}

func validateValue(value any, rule json.RawMessage, field string, depth int) error {
	if len(rule) == 0 {
		return nil
	}
	if depth > 64 {
		return fmt.Errorf("%w: nesting limit", ErrInvalidInput)
	}
	var s validationSchema
	if err := json.Unmarshal(rule, &s); err != nil {
		return fmt.Errorf("january: invalid generated schema")
	}
	bad := func() error { return fmt.Errorf("%w: %s does not match the contract", ErrInvalidInput, field) }
	if value == nil && s.Nullable {
		return nil
	}
	if s.Ref != "" {
		return validateValue(value, generatedSchemas[strings.TrimPrefix(s.Ref, "#/components/schemas/")], field, depth+1)
	}
	for _, child := range s.AllOf {
		if err := validateValue(value, child, field, depth+1); err != nil {
			return err
		}
	}
	if value == nil {
		return bad()
	}
	if len(s.Enum) > 0 {
		found := false
		for _, v := range s.Enum {
			if value == v {
				found = true
				break
			}
		}
		if !found {
			return bad()
		}
	}
	switch s.Type {
	case "string":
		v, ok := value.(string)
		if !ok {
			return bad()
		}
		n := utf8.RuneCountInString(v)
		if (s.MinLength != nil && n < *s.MinLength) || (s.MaxLength != nil && n > *s.MaxLength) {
			return bad()
		}
		if s.Pattern != "" {
			re, err := regexp.Compile(s.Pattern)
			if err == nil && !re.MatchString(v) {
				return bad()
			}
		}
		if s.Format == "date" {
			if _, err := time.Parse("2006-01-02", v); err != nil {
				return bad()
			}
		}
		if s.Format == "date-time" {
			if _, err := time.Parse(time.RFC3339Nano, v); err != nil {
				return bad()
			}
		}
	case "number", "integer":
		v, ok := value.(float64)
		if !ok {
			return bad()
		}
		belowMinimum := s.Minimum != nil && (v < *s.Minimum || (s.ExclusiveMinimum && v == *s.Minimum))
		aboveMaximum := s.Maximum != nil && (v > *s.Maximum || (s.ExclusiveMaximum && v == *s.Maximum))
		if (s.Type == "integer" && math.Trunc(v) != v) || belowMinimum || aboveMaximum {
			return bad()
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return bad()
		}
	case "array":
		v, ok := value.([]any)
		if !ok {
			return bad()
		}
		if (s.MinItems != nil && len(v) < *s.MinItems) || (s.MaxItems != nil && len(v) > *s.MaxItems) {
			return bad()
		}
		for _, item := range v {
			if err := validateValue(item, s.Items, field+"[]", depth+1); err != nil {
				return err
			}
		}
	case "object":
		v, ok := value.(map[string]any)
		if !ok {
			return bad()
		}
		for _, key := range s.Required {
			if _, exists := v[key]; !exists {
				return fmt.Errorf("%w: %s.%s is required", ErrInvalidInput, field, key)
			}
		}
		for key, child := range s.Properties {
			if item, exists := v[key]; exists {
				if err := validateValue(item, child, field+"."+key, depth+1); err != nil {
					return err
				}
			}
		}
	}
	if object, ok := value.(map[string]any); ok && s.RangeByUnit != nil {
		return s.RangeByUnit.check(object, field)
	}
	return nil
}
