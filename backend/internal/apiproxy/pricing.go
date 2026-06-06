package apiproxy

// CalculateCost is intentionally a no-op in this branch. Backend-side model
// pricing can be restored separately without blocking the budget metadata and
// enforcement path.
func CalculateCost(model string, inputTokens, outputTokens int) int64 {
	return 0
}
