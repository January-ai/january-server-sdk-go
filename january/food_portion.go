package january

import (
	"encoding/json"
	"math"
)

// FoodPortionErrorCode identifies a local portion validation failure.
type FoodPortionErrorCode string

const (
	FoodPortionNoServings      FoodPortionErrorCode = "no_servings"
	FoodPortionServingNotFound FoodPortionErrorCode = "serving_not_found"
	FoodPortionInvalidServing  FoodPortionErrorCode = "invalid_serving"
	FoodPortionInvalidQuantity FoodPortionErrorCode = "invalid_quantity"
)

// FoodPortionError is a local validation error, inspectable with errors.As.
type FoodPortionError struct{ Code FoodPortionErrorCode }

func (e *FoodPortionError) Error() string { return "january: invalid food portion: " + string(e.Code) }

// FoodPortionOptions selects a serving and quantity. Zero-value Optional fields are
// omitted; Value(0) is supplied and does not silently select a default quantity.
type FoodPortionOptions struct {
	ServingID Optional[ServingID]
	Quantity  Optional[float64]
}

// FoodPortion is a locally calculated snapshot. Treat it as read-only; construct a
// new portion when changing quantity so nutrition and selection stay consistent.
// Selection is directly usable in generated food-log and glucose-prediction inputs.
type FoodPortion struct {
	FoodID           FoodID            `json:"food_id"`
	Serving          ServingOption     `json:"serving"`
	Quantity         float64           `json:"quantity"`
	Nutrition        NutritionFacts    `json:"nutrition"`
	TotalWeightGrams Optional[float64] `json:"total_weight_grams"`
	GlycemicIndex    Optional[float64] `json:"glycemic_index"`
	GlycemicLoad     Optional[float64] `json:"glycemic_load"`
	Selection        FoodLogInputFood  `json:"selection"`
}

// NewFoodPortion matches the client SDK's serving and nutrient calculations. It
// performs no HTTP requests, does not mutate food, and retains missing nutrients.
// An omitted serving selects the first primary serving, otherwise the first one.
// An omitted quantity defaults to that serving's quantity. Explicit null options
// are invalid because they do not supply an ID/quantity and are not omission.
func NewFoodPortion(food FoodSearchItem, options FoodPortionOptions) (*FoodPortion, error) {
	if len(food.Servings) == 0 {
		return nil, &FoodPortionError{Code: FoodPortionNoServings}
	}
	index := 0
	if options.ServingID.IsSet() {
		id, ok := options.ServingID.Get()
		if !ok {
			return nil, &FoodPortionError{Code: FoodPortionServingNotFound}
		}
		index = -1
		for i, serving := range food.Servings {
			if serving.ID != nil && *serving.ID == id {
				index = i
				break
			}
		}
		if index < 0 {
			return nil, &FoodPortionError{Code: FoodPortionServingNotFound}
		}
	} else {
		for i, serving := range food.Servings {
			if serving.IsPrimary != nil && *serving.IsPrimary {
				index = i
				break
			}
		}
	}
	serving := food.Servings[index]
	if serving.ID == nil || serving.Quantity == nil || serving.ScalingFactor == nil ||
		!positiveFinite(*serving.Quantity) || !positiveFinite(*serving.ScalingFactor) {
		return nil, &FoodPortionError{Code: FoodPortionInvalidServing}
	}
	quantity := *serving.Quantity
	if options.Quantity.IsSet() {
		value, ok := options.Quantity.Get()
		if !ok {
			return nil, &FoodPortionError{Code: FoodPortionInvalidQuantity}
		}
		quantity = value
	}
	if !positiveFinite(quantity) || quantity > 10000 {
		return nil, &FoodPortionError{Code: FoodPortionInvalidQuantity}
	}
	scale := quantity * *serving.ScalingFactor / *serving.Quantity
	portion := &FoodPortion{
		FoodID: food.ID, Serving: cloneServingOption(serving), Quantity: quantity,
		Nutrition:     scalePortionNutrition(food.Nutrients, scale),
		GlycemicIndex: optionalPortionNumber(food.GlycemicIndex, 1),
		GlycemicLoad:  scalePortionNumber(food.GlycemicLoad, scale),
		Selection:     FoodLogInputFood{FoodID: food.ID, ServingID: *serving.ID, Quantity: quantity},
	}
	if serving.WeightGrams != nil {
		// Copy the model's pointer so neither snapshot aliases the caller's weight.
		weight := *serving.WeightGrams
		portion.TotalWeightGrams = Value(weight * quantity / *serving.Quantity)
	}
	return portion, nil
}

// Portion is the convenience form of NewFoodPortion. It makes no network requests.
func (food FoodSearchItem) Portion(options FoodPortionOptions) (*FoodPortion, error) {
	return NewFoodPortion(food, options)
}

func positiveFinite(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func optionalPortionNumber(value *float64, scale float64) Optional[float64] {
	if value == nil {
		return Optional[float64]{}
	}
	return Value(*value * scale)
}

func scalePortionNumber(value *float64, scale float64) Optional[float64] {
	return optionalPortionNumber(value, scale)
}

func cloneServingOption(serving ServingOption) ServingOption {
	clone := serving
	if serving.ID != nil {
		value := *serving.ID
		clone.ID = &value
	}
	if serving.Quantity != nil {
		value := *serving.Quantity
		clone.Quantity = &value
	}
	if serving.Unit != nil {
		value := *serving.Unit
		clone.Unit = &value
	}
	if serving.ScalingFactor != nil {
		value := *serving.ScalingFactor
		clone.ScalingFactor = &value
	}
	if serving.WeightGrams != nil {
		value := *serving.WeightGrams
		clone.WeightGrams = &value
	}
	if serving.IsPrimary != nil {
		value := *serving.IsPrimary
		clone.IsPrimary = &value
	}
	return clone
}

func scalePortionAmount(value Optional[NutrientAmount], scale float64) Optional[NutrientAmount] {
	if amount, ok := value.Get(); ok {
		return Value(NutrientAmount{Value: amount.Value * scale, Unit: amount.Unit})
	}
	return value
}

func scalePortionNutrition(n NutritionFacts, scale float64) NutritionFacts {
	return NutritionFacts{
		Calories:         scalePortionAmount(n.Calories, scale),
		Protein:          scalePortionAmount(n.Protein, scale),
		Carbohydrates:    scalePortionAmount(n.Carbohydrates, scale),
		NetCarbohydrates: scalePortionAmount(n.NetCarbohydrates, scale),
		TotalFat:         scalePortionAmount(n.TotalFat, scale),
		TransFat:         scalePortionAmount(n.TransFat, scale),
		SaturatedFat:     scalePortionAmount(n.SaturatedFat, scale),
		Fiber:            scalePortionAmount(n.Fiber, scale),
		TotalSugars:      scalePortionAmount(n.TotalSugars, scale),
		AddedSugars:      scalePortionAmount(n.AddedSugars, scale),
		Cholesterol:      scalePortionAmount(n.Cholesterol, scale),
		Calcium:          scalePortionAmount(n.Calcium, scale),
		Iron:             scalePortionAmount(n.Iron, scale),
		Potassium:        scalePortionAmount(n.Potassium, scale),
		Sodium:           scalePortionAmount(n.Sodium, scale),
		VitaminD:         scalePortionAmount(n.VitaminD, scale),
	}
}

// MarshalJSON preserves the generated models' absent/explicit-null distinction.
func (p FoodPortion) MarshalJSON() ([]byte, error) {
	fields := map[string]any{"food_id": p.FoodID, "serving": p.Serving, "quantity": p.Quantity, "nutrition": p.Nutrition, "selection": p.Selection}
	putOptional(fields, "total_weight_grams", p.TotalWeightGrams)
	putOptional(fields, "glycemic_index", p.GlycemicIndex)
	putOptional(fields, "glycemic_load", p.GlycemicLoad)
	return json.Marshal(fields)
}

func (p FoodPortion) String() string   { return "january.FoodPortion{[REDACTED]}" }
func (p FoodPortion) GoString() string { return p.String() }
