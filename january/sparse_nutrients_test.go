package january

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSparseNutrientResponses(t *testing.T) {
	data, err := os.ReadFile("testdata/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var bundle struct {
		Operations        []contractFixture
		NutrientResponses []struct {
			Name              string
			OperationID       string
			Valid             bool
			Response          wireFixture
			NutrientPaths     [][]any
			ExpectedNutrients map[string]any
		}
	}
	if err = json.Unmarshal(data, &bundle); err != nil {
		t.Fatal(err)
	}
	if len(bundle.NutrientResponses) != 16 {
		t.Fatalf("expected 16 generated nutrient cases, got %d", len(bundle.NutrientResponses))
	}
	bindings := map[string]contractFixture{}
	for _, f := range bundle.Operations {
		bindings[f.OperationID] = f
	}
	for _, fixture := range bundle.NutrientResponses {
		t.Run(fixture.OperationID+"/"+fixture.Name, func(t *testing.T) {
			base, ok := bindings[fixture.OperationID]
			if !ok {
				t.Fatal("missing generated operation binding")
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				path := base.Path
				for key, raw := range base.Request.Parameters["path"] {
					parts, err := parameterStrings(raw)
					if err != nil {
						t.Error(err)
						continue
					}
					path = strings.ReplaceAll(path, "{"+key+"}", url.PathEscape(parts[0]))
				}
				if r.Method != base.Method || r.URL.EscapedPath() != path {
					t.Error("request did not use fixture binding")
				}
				for key, value := range fixture.Response.Headers {
					w.Header().Set(key, value)
				}
				w.WriteHeader(fixture.Response.Status)
				_, _ = w.Write(fixture.Response.Body)
			}))
			defer server.Close()
			client, err := NewClient(Config{SecretKey: "sk-offline-nutrient-fixture", BaseURL: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			input := map[string]json.RawMessage{}
			if len(base.Request.Body) > 0 {
				if err = json.Unmarshal(base.Request.Body, &input); err != nil {
					t.Fatal(err)
				}
			}
			for _, parameters := range base.Request.Parameters {
				for key, value := range parameters {
					input[key] = value
				}
			}
			raw, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			value, metadata, err := invokeFixture(client, fixture.OperationID, raw)
			if calls.Load() != 1 {
				t.Fatalf("expected one request and no retry, got %d", calls.Load())
			}
			headers := http.Header{}
			for key, value := range fixture.Response.Headers {
				headers.Set(key, value)
			}
			if metadata == nil || metadata.StatusCode != fixture.Response.Status || metadata.RequestID == "" || metadata.RequestID != headers.Get("X-Request-ID") {
				t.Fatal("response metadata/request ID lost")
			}
			if !fixture.Valid {
				var validationError *TransportError
				if !errors.As(err, &validationError) || validationError.Kind != "invalid JSON response" || validationError.Cause == nil {
					t.Fatalf("missing required amount field was not rejected: %v", err)
				}
				if !strings.Contains(validationError.Cause.Error(), "required response field missing") {
					t.Fatalf("unexpected validation failure: %v", validationError.Cause)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var roundTrip any
			if err = json.Unmarshal(encoded, &roundTrip); err != nil {
				t.Fatal(err)
			}
			if len(fixture.NutrientPaths) == 0 {
				t.Fatal("missing nutrient assertions")
			}
			for _, path := range fixture.NutrientPaths {
				got := jsonNutrientPath(t, roundTrip, path)
				if !reflect.DeepEqual(got, fixture.ExpectedNutrients) {
					t.Fatalf("omitted nutrients/zero did not round-trip: got %v want %v", got, fixture.ExpectedNutrients)
				}
				assertNativeNutrientPresence(t, value, path, fixture.ExpectedNutrients)
			}
		})
	}
}

func jsonNutrientPath(t *testing.T, value any, path []any) any {
	t.Helper()
	for _, part := range path {
		switch key := part.(type) {
		case string:
			object, ok := value.(map[string]any)
			if !ok {
				t.Fatal("expected nutrient path object")
			}
			value, ok = object[key]
			if !ok {
				t.Fatal("missing nutrient path field")
			}
		case float64:
			array, ok := value.([]any)
			if !ok || key < 0 || int(key) >= len(array) {
				t.Fatal("invalid nutrient array index")
			}
			value = array[int(key)]
		default:
			t.Fatal("invalid generated nutrient path")
		}
	}
	return value
}

func unwrapNutrientValue(t *testing.T, value reflect.Value) reflect.Value {
	t.Helper()
	for {
		if value.Kind() == reflect.Pointer {
			if value.IsNil() {
				t.Fatal("nil nutrient value")
			}
			value = value.Elem()
			continue
		}
		if strings.HasPrefix(value.Type().Name(), "Optional[") {
			parts := value.MethodByName("Get").Call(nil)
			if !parts[1].Bool() {
				t.Fatal("expected supplied nutrient map")
			}
			value = parts[0]
			continue
		}
		return value
	}
}

// Inspect the actual SDK result, not a re-decoded model: omitted Optional fields must
// remain unset, while a real zero must remain a supplied NutrientAmount.Value.
func assertNativeNutrientPresence(t *testing.T, result any, path []any, expected map[string]any) {
	t.Helper()
	value := reflect.ValueOf(result)
	for _, part := range path {
		value = unwrapNutrientValue(t, value)
		switch key := part.(type) {
		case string:
			if value.Kind() != reflect.Struct {
				t.Fatal("expected generated model struct")
			}
			found := false
			for i := 0; i < value.NumField(); i++ {
				if strings.Split(value.Type().Field(i).Tag.Get("json"), ",")[0] == key {
					value = value.Field(i)
					found = true
					break
				}
			}
			if !found {
				t.Fatal("missing generated nutrient field")
			}
		case float64:
			if value.Kind() != reflect.Slice || key < 0 || int(key) >= value.Len() {
				t.Fatal("invalid generated nutrient slice")
			}
			value = value.Index(int(key))
		}
	}
	value = unwrapNutrientValue(t, value)
	if value.Kind() != reflect.Struct {
		t.Fatal("expected typed nutrient map")
	}
	for i := 0; i < value.NumField(); i++ {
		key := strings.Split(value.Type().Field(i).Tag.Get("json"), ",")[0]
		field := value.Field(i)
		isSet := field.MethodByName("IsSet")
		if !isSet.IsValid() {
			t.Fatalf("nutrient %s must be Optional", key)
		}
		_, present := expected[key]
		if isSet.Call(nil)[0].Bool() != present {
			t.Fatalf("wrong Optional.IsSet for %s", key)
		}
		if present {
			parts := field.MethodByName("Get").Call(nil)
			if !parts[1].Bool() {
				t.Fatalf("supplied nutrient %s lost", key)
			}
			amount, ok := parts[0].Interface().(NutrientAmount)
			if !ok {
				t.Fatal("expected typed nutrient amount")
			}
			expectedAmount := expected[key].(map[string]any)
			if amount.Value != expectedAmount["value"].(float64) || amount.Unit != expectedAmount["unit"] {
				t.Fatalf("amount/real zero not retained for %s", key)
			}
		}
	}
}

func TestResponseRequiredFieldsOnly(t *testing.T) {
	rule := json.RawMessage(`{"type":"object","required":["items","future"],"properties":{"items":{"type":"array","items":{"allOf":[{"$ref":"#/components/schemas/NutrientAmount"}]}},"future":{"type":"string","enum":["old"],"format":"date-time","maxLength":1},"score":{"type":"number","minimum":10}}}`)
	valid := json.RawMessage(`{"items":[{"value":0,"unit":"unknown-future-unit","new_field":true}],"future":"new-enum-value","score":0,"extra":{"value":"ignored"}}`)
	if err := validateResponseRequired(valid, rule); err != nil {
		t.Fatalf("request-only restrictions rejected forward-compatible response: %v", err)
	}
	for _, raw := range []string{`{"items":[],"score":0}`, `{"items":[{"unit":"g"}],"future":"new"}`, `{"items":[{"value":0}],"future":"new"}`} {
		if err := validateResponseRequired(json.RawMessage(raw), rule); err == nil {
			t.Fatal("missing required response field accepted")
		}
	}
}
