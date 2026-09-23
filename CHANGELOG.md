# Changelog

## Unreleased

## 0.2.0 - 2026-09-22

### Breaking changes

The v1.2 API changed the shape of analysis results, logged foods and scan
corrections. Code written against 0.1.0 needs these updates to compile:

- `ServingDetails` is removed. `LoggedFood.Serving` is now a `ServingSummary`, the
  same serving type analysis results use, including `WeightGrams`.
- `ServingSummary.ID` is a `string` and `ServingSummary.Quantity` a `float64`
  (were `*string` and `*float64`); the API now always returns both.
- `ServingOption.ID`, `AlternativeFood.ID`, `RestaurantMenuItem.ID` and
  `LoggedFood.FoodID` are `string` (were `*string`); the API always returns them.
  `ServingSummary.WeightGrams` (`*float64`, nil when unknown) is new.
- `DetectedFood.ID` is a `string` and `DetectedFood.Quantity` a `float64` (were
  `*string` and `*float64`).
- `CorrectPhotoScanRequest.Analysis` is a `CorrectionAnalysis` instead of a
  `FoodScan`. Build it with `scan.Correction()`, or call
  `FoodAnalysis.CorrectScan(ctx, scan, instruction)`.
- `GlucosePredictionProfile.Age` is an `int64` (was `float64`); the API takes whole
  years.

### Added

- Water logs: `WaterLogs.Create`, `WaterLogs.List` (one total per local day in the
  requested unit) and `WaterLogs.Delete` (deleting an unknown log also succeeds).
  Amounts are in `VolumeUnitFlOz`, `VolumeUnitCup` or `VolumeUnitMl`.
- Weight logs: `WeightLogs.Create` and `WeightLogs.List` (latest weight per local
  day). The API has no weight-log deletion.
- The `ScopeWaterLogsRead`, `ScopeWaterLogsWrite`, `ScopeWeightLogsRead` and
  `ScopeWeightLogsWrite` client-token scopes.
- `FoodScan.Correction` and `FoodAnalysisService.CorrectScan`, which send a returned
  scan back for correction without dropping any field.
- `CreditPlan`, `NutrientUnit` and `VolumeUnit` string types with constants for the
  documented values; unknown values the API adds later are kept as returned.

### Changed

- Food, water and weight logs send and read their time as `created_at`, the name
  the API now uses in requests and replies in place of `eaten_at`, `consumed_at`
  and `measured_at`. The public fields keep their names (`EatenAt`, `ConsumedAt`
  and `MeasuredAt`), so no code changes are needed. 0.1.0 still sends and expects
  `eaten_at` for food logs.
- A water amount must be within its unit's range (1–811.5 fl oz, 0.1–101.4 cups,
  30–24000 ml), a weight log within 10–1000 lb or 4.5–453.6 kg, a glucose profile's
  weight within 2–1500 lb or 1–700 kg and its height within 20–108 in or 50–275 cm,
  and a food or serving quantity must be
  greater than zero, as the API requires. These are checked before any request is
  sent and return `ErrInvalidInput`.
- Photo analysis uses the reasoning-based analyzer when `Reasoning` is not set, as
  the API now defaults to it. Set `Reasoning` with effort `none` for the standard
  analyzer. The SDK sends `Reasoning` only when you set it.
- Food and serving IDs must be 1–10 digits without a leading zero; other values
  return `ErrInvalidInput` before any request. The `conflict` error code (409) is
  never retried.
- Token creation and food, water and weight-log creation are never replayed after
  an ambiguous failure (a timeout, lost response or 5xx reply), because the API may
  already have recorded the write. A 429 `rate_limited` reply recorded nothing, so
  it is retried within the configured limits like any other request.
- `FoodLogs.Update` rejects an update that sets no field before sending it, and
  sends only the fields you set.

## 0.1.0 - 2026-09-16

Initial public release.
