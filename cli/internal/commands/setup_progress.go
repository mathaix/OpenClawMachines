package commands

// setupStep represents a step in the setup wizard.
type setupStep struct {
	Name string
}

var setupSteps = []setupStep{
	{Name: "Login"},
	{Name: "Machine"},
	{Name: "Provider"},
	{Name: "Channel"},
	{Name: "Identity"},
	{Name: "Verify"},
}

// printStepProgress renders a single-line progress indicator for the setup wizard.
// Delegates to printStyledProgress for styled output.
func printStepProgress(currentIdx int, completed map[int]bool) {
	printStyledProgress(currentIdx, completed)
}
