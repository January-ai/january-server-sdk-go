package january

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"
)

func portionTestFood() FoodSearchItem {
	primaryWeight, alternateWeight := 50.0, 120.0
	return FoodSearchItem{
		ID: "42", Name: portionPointer("Test food"),
		Nutrients:     NutritionFacts{Calories: Value(NutrientAmount{Value: 100, Unit: "cal"}), Protein: Value(NutrientAmount{Value: 10, Unit: "g"})},
		GlycemicIndex: portionPointer(50.0), GlycemicLoad: portionPointer(8.0),
		Servings: []ServingOption{
			{ID: portionPointer("1"), Quantity: portionPointer(1.0), Unit: portionPointer("slice"), ScalingFactor: portionPointer(1.0), WeightGrams: &primaryWeight, IsPrimary: portionPointer(true)},
			{ID: portionPointer("2"), Quantity: portionPointer(2.0), Unit: portionPointer("pieces"), ScalingFactor: portionPointer(3.0), WeightGrams: &alternateWeight},
		},
	}
}

func portionPointer[T any](value T) *T { return &value }

func requirePortion(t *testing.T, food FoodSearchItem, options FoodPortionOptions) *FoodPortion {
	t.Helper()
	p, err := NewFoodPortion(food, options)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func requirePortionNumber(t *testing.T, got Optional[float64], want float64) {
	t.Helper()
	value, present := got.Get()
	if !present || math.Abs(value-want) > 0.001 {
		t.Fatalf("expected %g, got %g (present=%t)", want, value, present)
	}
}

func requirePortionAmount(t *testing.T, got Optional[NutrientAmount], want float64, unit string) {
	t.Helper()
	amount, present := got.Get()
	if !present || math.Abs(amount.Value-want) > 0.001 || amount.Unit != unit {
		t.Fatalf("expected %g %s, got %g %s (present=%t)", want, unit, amount.Value, amount.Unit, present)
	}
}

func TestFoodPortionClientPrimary(t *testing.T) {
	p, err := portionTestFood().Portion(FoodPortionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if p.FoodID != "42" || p.Serving.ID == nil || *p.Serving.ID != "1" || p.Quantity != 1 {
		t.Fatal("wrong default serving")
	}
	requirePortionAmount(t, p.Nutrition.Calories, 100, "cal")
	want := FoodLogInputFood{FoodID: "42", ServingID: "1", Quantity: 1}
	if p.Selection != want {
		t.Fatal("wrong request-ready selection")
	}
}

func TestFoodPortionClientAlternate(t *testing.T) {
	p := requirePortion(t, portionTestFood(), FoodPortionOptions{ServingID: Value(ServingID("2")), Quantity: Value(4.0)})
	requirePortionAmount(t, p.Nutrition.Calories, 600, "cal")
	requirePortionAmount(t, p.Nutrition.Protein, 60, "g")
	requirePortionNumber(t, p.TotalWeightGrams, 240)
	requirePortionNumber(t, p.GlycemicIndex, 50)
	requirePortionNumber(t, p.GlycemicLoad, 48)
	if p.Serving.Quantity == nil || *p.Serving.Quantity != 2 || p.Quantity != 4 {
		t.Fatal("serving metadata was replaced by requested quantity")
	}
	logRequest := CreateFoodLogRequest{Foods: []FoodLogInputFood{p.Selection}}
	glucoseRequest := PredictGlucoseRequest{Foods: []FoodLogInputFood{p.Selection}}
	if logRequest.Foods[0] != glucoseRequest.Foods[0] {
		t.Fatal("selection incompatible with generated inputs")
	}
	encoded, err := json.Marshal(p.Selection)
	if err != nil {
		t.Fatal(err)
	}
	equalJSON(t, encoded, []byte(`{"food_id":"42","serving_id":"2","quantity":4}`))
}

func TestFoodPortionIOSReference(t *testing.T) {
	weight := 100.0
	food := FoodSearchItem{ID: "70381819", Name: portionPointer("banana"), Nutrients: NutritionFacts{
		Calories: Value(NutrientAmount{Value: 105.02, Unit: "cal"}), Protein: Value(NutrientAmount{Value: 1.2862, Unit: "g"}),
		Carbohydrates: Value(NutrientAmount{Value: 26.9512, Unit: "g"}), Potassium: Value(NutrientAmount{Value: 422, Unit: "mg"}),
	}, GlycemicIndex: portionPointer(51.0), GlycemicLoad: portionPointer(12.0), Servings: []ServingOption{{ID: portionPointer("2"), Quantity: portionPointer(100.0), Unit: portionPointer("g"), ScalingFactor: portionPointer(0.8474576271), WeightGrams: &weight}}}
	p := requirePortion(t, food, FoodPortionOptions{ServingID: Value(ServingID("2")), Quantity: Value(200.0)})
	requirePortionAmount(t, p.Nutrition.Calories, 178, "cal")
	requirePortionAmount(t, p.Nutrition.Protein, 2.18, "g")
	requirePortionAmount(t, p.Nutrition.Carbohydrates, 45.68, "g")
	requirePortionAmount(t, p.Nutrition.Potassium, 715.254, "mg")
	requirePortionNumber(t, p.TotalWeightGrams, 200)
	requirePortionNumber(t, p.GlycemicIndex, 51)
	requirePortionNumber(t, p.GlycemicLoad, 20.3389)
}

func TestFoodPortionDefaultFallback(t *testing.T) {
	t.Run("first primary", func(t *testing.T) {
		food := portionTestFood()
		food.Servings[0].IsPrimary = portionPointer(false)
		food.Servings[1].IsPrimary = portionPointer(true)
		food.Servings = append(food.Servings, ServingOption{ID: portionPointer("3"), Quantity: portionPointer(1.0), ScalingFactor: portionPointer(1.0), IsPrimary: portionPointer(true)})
		p := requirePortion(t, food, FoodPortionOptions{})
		if p.Serving.ID == nil || *p.Serving.ID != "2" || p.Quantity != 2 {
			t.Fatal("did not select first primary/default quantity")
		}
		requirePortionAmount(t, p.Nutrition.Calories, 300, "cal")
	})
	t.Run("first when no primary", func(t *testing.T) {
		food := portionTestFood()
		food.Servings[0].IsPrimary = portionPointer(false)
		p := requirePortion(t, food, FoodPortionOptions{})
		if p.Serving.ID == nil || *p.Serving.ID != "1" {
			t.Fatal("did not fall back to first serving")
		}
	})
	t.Run("first exact ID match", func(t *testing.T) {
		food := portionTestFood()
		food.Servings = append(food.Servings, ServingOption{ID: portionPointer("2"), Quantity: portionPointer(10.0), ScalingFactor: portionPointer(1.0)})
		p := requirePortion(t, food, FoodPortionOptions{ServingID: Value(ServingID("2"))})
		if p.Quantity != 2 {
			t.Fatal("did not select first exact match")
		}
	})
}

func TestFoodPortionAllNutrients(t *testing.T) {
	keys := []string{"calories", "protein", "carbohydrates", "net_carbohydrates", "total_fat", "trans_fat", "saturated_fat", "fiber", "total_sugars", "added_sugars", "cholesterol", "calcium", "iron", "potassium", "sodium", "vitamin_d"}
	if reflect.TypeOf(NutritionFacts{}).NumField() != len(keys) {
		t.Fatal("update all-nutrient coverage for generated model")
	}
	input := map[string]NutrientAmount{}
	for i, key := range keys {
		input[key] = NutrientAmount{Value: float64(i + 1), Unit: fmt.Sprintf("unit-%d", i)}
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	food := portionTestFood()
	if err = json.Unmarshal(encoded, &food.Nutrients); err != nil {
		t.Fatal(err)
	}
	p := requirePortion(t, food, FoodPortionOptions{ServingID: Value(ServingID("2")), Quantity: Value(0.5)})
	encoded, err = json.Marshal(p.Nutrition)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]NutrientAmount
	if err = json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 16 {
		t.Fatal("not all 16 nutrients scaled")
	}
	for _, key := range keys {
		if got[key].Value != input[key].Value*0.75 || got[key].Unit != input[key].Unit {
			t.Errorf("wrong scale/unit for %s", key)
		}
	}
}

func TestFoodPortionMissingAndZero(t *testing.T) {
	food := portionTestFood()
	food.Nutrients = NutritionFacts{Protein: Value(NutrientAmount{Value: 0, Unit: "g"})}
	food.GlycemicIndex = nil
	food.GlycemicLoad = nil
	food.Servings[0].WeightGrams = nil
	p := requirePortion(t, food, FoodPortionOptions{Quantity: Value(2.0)})
	requirePortionAmount(t, p.Nutrition.Protein, 0, "g")
	if p.Nutrition.Calories.IsSet() || p.Nutrition.Fiber.IsSet() || p.TotalWeightGrams.IsSet() || p.GlycemicIndex.IsSet() || p.GlycemicLoad.IsSet() {
		t.Fatal("missing value synthesized")
	}
	encoded, err := json.Marshal(p.Nutrition)
	if err != nil {
		t.Fatal(err)
	}
	equalJSON(t, encoded, []byte(`{"protein":{"value":0,"unit":"g"}}`))
	food.Nutrients = NutritionFacts{}
	p = requirePortion(t, food, FoodPortionOptions{})
	encoded, err = json.Marshal(p.Nutrition)
	if err != nil {
		t.Fatal(err)
	}
	equalJSON(t, encoded, []byte(`{}`))
	zero := 0.0
	food.Servings[0].WeightGrams = &zero
	food.GlycemicIndex = portionPointer(0.0)
	food.GlycemicLoad = portionPointer(0.0)
	p = requirePortion(t, food, FoodPortionOptions{})
	requirePortionNumber(t, p.TotalWeightGrams, 0)
	requirePortionNumber(t, p.GlycemicIndex, 0)
	requirePortionNumber(t, p.GlycemicLoad, 0)
}

func TestFoodPortionInvalidInputs(t *testing.T) {
	tests := []struct {
		name    string
		modify  func(*FoodSearchItem)
		options FoodPortionOptions
		code    FoodPortionErrorCode
	}{
		{name: "no servings", modify: func(f *FoodSearchItem) { f.Servings = nil }, code: FoodPortionNoServings},
		{name: "empty servings", modify: func(f *FoodSearchItem) { f.Servings = []ServingOption{} }, code: FoodPortionNoServings},
		{name: "unknown serving", options: FoodPortionOptions{ServingID: Value(ServingID("999"))}, code: FoodPortionServingNotFound},
		{name: "explicit null serving", options: FoodPortionOptions{ServingID: Null[ServingID]()}, code: FoodPortionServingNotFound},
		{name: "explicit null quantity", options: FoodPortionOptions{Quantity: Null[float64]()}, code: FoodPortionInvalidQuantity},
		{name: "default quantity too large", modify: func(f *FoodSearchItem) { f.Servings[0].Quantity = portionPointer(10001.0) }, code: FoodPortionInvalidQuantity},
	}
	for _, bad := range []struct {
		name  string
		value float64
	}{{"zero", 0}, {"negative", -1}, {"NaN", math.NaN()}, {"infinity", math.Inf(1)}, {"negative infinity", math.Inf(-1)}} {
		value := bad.value
		tests = append(tests,
			struct {
				name    string
				modify  func(*FoodSearchItem)
				options FoodPortionOptions
				code    FoodPortionErrorCode
			}{name: "quantity " + bad.name, options: FoodPortionOptions{Quantity: Value(value)}, code: FoodPortionInvalidQuantity},
			struct {
				name    string
				modify  func(*FoodSearchItem)
				options FoodPortionOptions
				code    FoodPortionErrorCode
			}{name: "serving quantity " + bad.name, modify: func(f *FoodSearchItem) { f.Servings[0].Quantity = portionPointer(value) }, code: FoodPortionInvalidServing},
			struct {
				name    string
				modify  func(*FoodSearchItem)
				options FoodPortionOptions
				code    FoodPortionErrorCode
			}{name: "scaling factor " + bad.name, modify: func(f *FoodSearchItem) { f.Servings[0].ScalingFactor = portionPointer(value) }, code: FoodPortionInvalidServing},
		)
	}
	tests = append(tests, struct {
		name    string
		modify  func(*FoodSearchItem)
		options FoodPortionOptions
		code    FoodPortionErrorCode
	}{name: "quantity above limit", options: FoodPortionOptions{Quantity: Value(10000.01)}, code: FoodPortionInvalidQuantity})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			food := portionTestFood()
			if test.modify != nil {
				test.modify(&food)
			}
			p, err := NewFoodPortion(food, test.options)
			var typed *FoodPortionError
			if p != nil || err == nil || !errors.As(fmt.Errorf("consumer: %w", err), &typed) || typed.Code != test.code {
				t.Fatalf("expected %s, got %v", test.code, err)
			}
		})
	}
}

func TestFoodPortionQuantityBoundaries(t *testing.T) {
	for _, quantity := range []float64{0.125, 10000} {
		p := requirePortion(t, portionTestFood(), FoodPortionOptions{Quantity: Value(quantity)})
		if p.Quantity != quantity || p.Selection.Quantity != quantity {
			t.Fatal("valid quantity changed")
		}
	}
}

func TestFoodPortionDoesNotMutate(t *testing.T) {
	food := portionTestFood()
	before, err := json.Marshal(food)
	if err != nil {
		t.Fatal(err)
	}
	p := requirePortion(t, food, FoodPortionOptions{Quantity: Value(2.0)})
	after, err := json.Marshal(food)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("input mutated during calculation")
	}
	*p.Serving.WeightGrams = 999
	p.Nutrition.Calories = Value(NutrientAmount{Value: 999, Unit: "changed"})
	p.Selection.Quantity = 999
	if *food.Servings[0].WeightGrams != 50 || food.Servings[0].Quantity == nil || *food.Servings[0].Quantity != 1 {
		t.Fatal("portion aliases source serving")
	}
	requirePortionAmount(t, food.Nutrients.Calories, 100, "cal")
	p = requirePortion(t, food, FoodPortionOptions{})
	*food.Servings[0].WeightGrams = 88
	if *p.Serving.WeightGrams != 50 {
		t.Fatal("source mutation changed portion snapshot")
	}
}
