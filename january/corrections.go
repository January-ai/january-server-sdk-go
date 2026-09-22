package january

import (
	"context"
	"fmt"
)

// Correction converts a scan returned by AnalyzePhoto or AnalyzeDescription into
// the analysis a correction request sends back. Every field is carried across
// unchanged: meal name, totals, and each detection's confidence, food, quantity,
// serving (including its gram weight) and nutrients.
func (s FoodScan) Correction() CorrectionAnalysis {
	analysis := CorrectionAnalysis{
		MealName:       Null[string](),
		TotalNutrients: Value(s.TotalNutrients),
		Detections:     make([]CorrectionDetection, 0, len(s.Detections)),
	}
	if s.MealName != nil {
		analysis.MealName = Value(*s.MealName)
	}
	for _, detection := range s.Detections {
		serving := CorrectionServing{ID: detection.Food.Serving.ID, Quantity: detection.Food.Serving.Quantity, Unit: detection.Food.Serving.Unit}
		if detection.Food.Serving.WeightGrams == nil {
			serving.WeightGrams = Null[float64]()
		} else {
			serving.WeightGrams = Value(*detection.Food.Serving.WeightGrams)
		}
		analysis.Detections = append(analysis.Detections, CorrectionDetection{
			Confidence: detection.Confidence,
			Food: CorrectionFood{
				Name:      detection.Food.Name,
				BrandName: detection.Food.BrandName,
				ID:        detection.Food.ID,
				Quantity:  detection.Food.Quantity,
				Nutrients: detection.Food.Nutrients,
				Serving:   serving,
			},
		})
	}
	return analysis
}

// CorrectScan sends a scan back with a plain-English instruction. It is
// Correct with the analysis built by FoodScan.Correction.
func (c *FoodAnalysisService) CorrectScan(ctx context.Context, scan FoodScan, instruction string) (*FoodScan, *Response, error) {
	return c.Correct(ctx, CorrectPhotoScanRequest{Analysis: scan.Correction(), Instruction: instruction})
}

// neverReplayOnAmbiguousFailure lists writes that are not idempotent: a retry
// after an ambiguous failure could record the same entry twice.
var neverReplayOnAmbiguousFailure = map[string]bool{
	"createFoodLog":   true,
	"createWaterLog":  true,
	"createWeightLog": true,
}

func replayAllowed(op operation) bool {
	return op.RetryAmbiguous && !neverReplayOnAmbiguousFailure[op.ID]
}

// validateBody applies the request rules the wire schema states but a partial
// update cannot express through types alone.
func validateBody(op operation, body map[string]any) error {
	if op.ID == "updateFoodLog" && len(body) == 0 {
		return fmt.Errorf("%w: an update must set at least one of Foods, EatenAt or Name", ErrInvalidInput)
	}
	return nil
}
