package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/January-ai/january-server-sdk-go/january"
	"net/http"
	"net/http/httptest"
)

func main() {
	food := january.FoodSearchItem{
		ID: 42, Name: "Installed consumer fake food",
		Nutrients: january.NutritionFacts{Calories: january.Value(january.NutrientAmount{Value: 100, Unit: "kcal"})},
		Servings:  []january.ServingOption{{ID: 1, Quantity: 2, ScalingFactor: 3, IsPrimary: true}},
	}
	var options january.FoodPortionOptions
	options.Quantity = january.Value(4.0)
	var portion *january.FoodPortion
	portion, err := january.NewFoodPortion(food, options)
	if err != nil {
		panic(err)
	}
	calories, present := portion.Nutrition.Calories.Get()
	if !present || calories.Value != 600 || calories.Unit != "kcal" {
		panic("installed portion scaling failed")
	}
	logInput := january.CreateFoodLogRequest{Foods: []january.FoodLogInputFood{portion.Selection}}
	glucoseInput := january.PredictGlucoseRequest{Foods: []january.FoodLogInputFood{portion.Selection}}
	if logInput.Foods[0] != glucoseInput.Foods[0] || logInput.Foods[0].Serving.Quantity != 4 {
		panic("installed selection type mismatch")
	}
	if _, err = food.Portion(january.FoodPortionOptions{}); err != nil {
		panic(err)
	}
	_, err = food.Portion(january.FoodPortionOptions{Quantity: january.Value(0.0)})
	var portionError *january.FoodPortionError
	if !errors.As(err, &portionError) || portionError.Code != january.FoodPortionInvalidQuantity {
		panic("installed portion error type mismatch")
	}
	fmt.Println("Installed Go module consumer: FoodPortion exports and generated selections passed")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			EndUserID string `json:"end_user_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.EndUserID != "user" {
			panic("wrong request body")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"ct-installed","expires_in":300,"expires_at":"2026-08-30T18:30:00Z","end_user_id":"user","scopes":["foods:read"]}`))
	}))
	defer server.Close()
	client, err := january.NewClient(january.Config{SecretKey: "fixture", BaseURL: server.URL})
	if err != nil {
		panic(err)
	}
	token, err := client.ClientTokens.Create(context.Background(), january.CreateClientTokenInput{EndUserID: "user", Scopes: []january.ClientScope{january.ScopeFoodsRead}})
	if err != nil {
		panic(err)
	}
	if token.Token != "ct-installed" {
		panic("wrong token")
	}
	encoded, err := json.Marshal(token)
	if err != nil {
		panic(err)
	}
	if string(encoded) != `{"token":"ct-installed","expiresIn":300}` {
		panic(string(encoded))
	}
	fmt.Println("Installed Go module consumer: real HTTP token flow passed")
}
