// An entirely local portion calculation: no client, credentials, files, or HTTP.
package main

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/January-ai/january-server-sdk-go/january"
)

func main() {
	weight := 120.0
	food := january.FoodSearchItem{
		ID: 42, Name: "Synthetic example food",
		Nutrients: january.NutritionFacts{
			Calories: january.Value(january.NutrientAmount{Value: 100, Unit: "kcal"}),
			Protein:  january.Value(january.NutrientAmount{Value: 0, Unit: "g"}),
		},
		GlycemicIndex: january.Value(50.0), GlycemicLoad: january.Value(8.0),
		Servings: []january.ServingOption{{ID: 2, Quantity: 2, Unit: "pieces", ScalingFactor: 3, WeightGrams: &weight, IsPrimary: true}},
	}
	portion, err := january.NewFoodPortion(food, january.FoodPortionOptions{Quantity: january.Value(4.0)})
	if err != nil {
		log.Fatal(err)
	}
	calories, _ := portion.Nutrition.Calories.Get()
	protein, present := portion.Nutrition.Protein.Get()
	grams, _ := portion.TotalWeightGrams.Get()
	if calories.Value != 600 || !present || protein.Value != 0 || grams != 240 || portion.Nutrition.Fiber.IsSet() {
		log.Fatal("incorrect portion calculation")
	}
	// Both inputs use the exact generated selection type. No requests are made.
	logInput := january.CreateFoodLogRequest{Foods: []january.FoodLogInputFood{portion.Selection}}
	predictionInput := january.PredictGlucoseRequest{
		StartTime: "2026-08-30T12:00:00Z",
		UserProfile: january.GlucosePredictionProfile{
			Age: 30, Sex: january.SexMale,
			Height: january.Height{Value: 175, Unit: january.HeightUnitCm},
			Weight: january.Weight{Value: 70, Unit: january.WeightUnitKg},
		},
		Foods: []january.FoodLogInputFood{portion.Selection},
	}
	if logInput.Foods[0] != predictionInput.Foods[0] {
		log.Fatal("selection mismatch")
	}
	selection, err := json.Marshal(portion.Selection)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Offline portion: %.0f %s, protein %.0f %s, weight %.0f g; missing fiber preserved.\n", calories.Value, calories.Unit, protein.Value, protein.Unit, grams)
	fmt.Printf("Log/glucose selection: %s\n", selection)
}
