package apiproxy

// CalculateCost is a no-op — cost calculation has moved to the backend API
// which looks up pricing from the model_catalog database table.
// The proxy still logs token counts but cost is calculated server-side.
func CalculateCost(model string, inputTokens, outputTokens int) int64 {
	return 0
}
