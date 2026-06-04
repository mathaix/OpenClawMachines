package commands

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"golang.org/x/term"
)

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// ── ANSI escape codes ──

const (
	ansiReset = "\033[0m"
	ansiBold  = "\033[1m"
	ansiDim   = "\033[2m"

	ansiGreen = "\033[38;2;108;207;95m"  // #6ccf5f
	ansiBlue  = "\033[38;2;82;168;232m"  // #52a8e8
	ansiAmber = "\033[38;2;212;164;76m"  // #d4a44c
	ansiRed   = "\033[38;2;232;93;108m"  // #e85d6c
	ansiCyan  = "\033[38;2;86;200;216m"  // #56c8d8
	ansiWhite = "\033[38;2;232;232;232m" // #e8e8e8
	ansiMuted = "\033[38;2;99;109;126m"  // #636d7e
	ansiFaint = "\033[38;2;74;85;104m"   // #4a5568

	ansiBoxBorder = "\033[38;2;42;52;68m" // #2a3444
)

// useColor reports whether styled output is enabled.
// Disabled when NO_COLOR is set, stdout is not a terminal, or TERM=dumb.
func useColor() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// ── Color wrappers ──

func colorize(code, s string) string {
	if !useColor() {
		return s
	}
	return code + s + ansiReset
}

func cGreen(s string) string { return colorize(ansiGreen, s) }
func cBlue(s string) string  { return colorize(ansiBlue, s) }
func cAmber(s string) string { return colorize(ansiAmber, s) }
func cRed(s string) string   { return colorize(ansiRed, s) }
func cCyan(s string) string  { return colorize(ansiCyan, s) }
func cWhite(s string) string { return colorize(ansiWhite, s) }
func cMuted(s string) string { return colorize(ansiMuted, s) }
func cDim(s string) string   { return colorize(ansiFaint, s) }
func cBrand(s string) string { return colorize(ansiAmber+ansiBold, s) }
func cBold(s string) string  { return colorize(ansiWhite+ansiBold, s) }

func cBox(s string) string { return colorize(ansiBoxBorder, s) }

// visibleLen returns the display width of s, excluding ANSI escape sequences.
func visibleLen(s string) int {
	return len(ansiPattern.ReplaceAllString(s, ""))
}

// ── Check / warn / fail marks ──

func checkMark() string { return cGreen("✓") }
func warnMark() string  { return cAmber("!") }
func failMark() string  { return cRed("✗") }

// ── Box drawing ──

// wizardHeader prints the setup wizard header box.
//
//	╭───────────────────────────────────────────────────────╮
//	│          O P E N C L A W   M A C H I N E S          │
//	│                  setup wizard                       │
//	╰───────────────────────────────────────────────────────╯
func wizardHeader() string {
	const w = 55
	bar := strings.Repeat("─", w)
	title := "O P E N C L A W   M A C H I N E S"
	sub := "setup wizard"

	return fmt.Sprintf(
		"  %s\n  %s  %s  %s\n  %s  %s  %s\n  %s",
		cBox("╭"+bar+"╮"),
		cBox("│"), cBrand(centerPadStr(title, w-2)), cBox("│"),
		cBox("│"), cDim(centerPadStr(sub, w-2)), cBox("│"),
		cBox("╰"+bar+"╯"),
	)
}

// summaryBox wraps lines of text in a bordered box.
func summaryBox(lines []string) string {
	// Find max visible width (excluding ANSI codes)
	maxW := 0
	for _, l := range lines {
		if vl := visibleLen(l); vl > maxW {
			maxW = vl
		}
	}
	w := maxW + 4 // padding
	if w < 55 {
		w = 55
	}
	bar := strings.Repeat("─", w)

	var b strings.Builder
	b.WriteString(fmt.Sprintf("  %s\n", cBox("╭"+bar+"╮")))
	b.WriteString(fmt.Sprintf("  %s%s%s\n", cBox("│"), strings.Repeat(" ", w), cBox("│")))
	for _, l := range lines {
		// Pad based on visible width, not raw byte length
		pad := w - 2 - visibleLen(l)
		if pad < 0 {
			pad = 0
		}
		b.WriteString(fmt.Sprintf("  %s  %s%s%s\n", cBox("│"), l, strings.Repeat(" ", pad), cBox("│")))
	}
	b.WriteString(fmt.Sprintf("  %s%s%s\n", cBox("│"), strings.Repeat(" ", w), cBox("│")))
	b.WriteString(fmt.Sprintf("  %s", cBox("╰"+bar+"╯")))
	return b.String()
}

// doctorHeader prints a smaller header box for ocm doctor.
func doctorHeader() string {
	const w = 55
	bar := strings.Repeat("─", w)
	return fmt.Sprintf(
		"  %s\n  %s  %s  %s\n  %s",
		cBox("╭"+bar+"╮"),
		cBox("│"), cBrand(centerPadStr("OCM Doctor", w-2)), cBox("│"),
		cBox("╰"+bar+"╯"),
	)
}

// centerPadStr centers text in exactly `width` characters (ASCII safe).
func centerPadStr(text string, width int) string {
	tl := len(text)
	if tl >= width {
		return text[:width]
	}
	left := (width - tl) / 2
	right := width - tl - left
	return strings.Repeat(" ", left) + text + strings.Repeat(" ", right)
}

// ── Step progress rail ──

// printStyledProgress renders the step progress indicator:
//
//	● Login ─── ● Machine ─── ○ Provider ─── ○ Channel ─── ○ Identity ─── ○ Verify
func printStyledProgress(currentIdx int, completed map[int]bool) {
	var parts []string
	for i, step := range setupSteps {
		var part string
		switch {
		case completed[i]:
			part = cGreen("● " + step.Name)
		case i == currentIdx:
			part = cBlue("● " + step.Name)
		default:
			part = cDim("○ " + step.Name)
		}
		parts = append(parts, part)
	}
	fmt.Printf("\n  %s\n", strings.Join(parts, cDim(" ─── ")))
}

// ── Section headers ──

// sectionHeader prints a step header like:
//
//	STEP 3 · LLM PROVIDER
//	──────────────────────
func sectionHeader(stepNum int, title string, optional bool) {
	label := fmt.Sprintf("STEP %d", stepNum)
	suffix := ""
	if optional {
		suffix = " " + cDim("(optional)")
	}
	titleLine := fmt.Sprintf("  %s %s %s%s", cBold(label), cDim("·"), cBlue(title), suffix)
	divLen := len(label) + 3 + len(title)
	if optional {
		divLen += 11
	}
	fmt.Printf("\n\n%s\n  %s\n", titleLine, cDim(strings.Repeat("─", divLen)))
}

// ── Styled root help ──

// printStyledHelp prints the root command help in the prototype style.
func printStyledHelp() {
	fmt.Printf("\nOCM CLI — manage OpenClaw Machines %s\n", cDim("v"+Version))
	fmt.Println()
	fmt.Printf("%s %s\n", cBrand("  First time? Run:"), cBold("ocm setup"))
	fmt.Println()
	fmt.Println(cBold("Commands:"))

	type cmdEntry struct {
		name string
		desc string
		hi   bool // highlighted
	}
	cmds := []cmdEntry{
		{"setup", "Guided setup wizard — login, create machine, add providers", true},
		{"machines", "Manage OpenClaw Machines", false},
		{"providers", "Manage API provider credentials", false},
		{"channels", "Manage messaging channels", false},
		{"skills", "Manage machine skills", false},
		{"tools", "Manage machine tools", false},
		{"identity", "Manage machine identity", false},
		{"budget", "Manage spending budgets", false},
		{"usage", "Show LLM usage records", false},
		{"status", "Machine dashboard", false},
		{"doctor", "Diagnose configuration problems", false},
		{"config", "Manage CLI configuration", false},
		{"login", "Authenticate with the OCM API", false},
		{"completion", "Generate shell completions", false},
		{"version", "Print version info", false},
	}

	for _, c := range cmds {
		if c.hi {
			fmt.Printf("  %-13s%s\n", cGreen(c.name), c.desc)
		} else {
			fmt.Printf("  %-13s%s\n", cMuted(c.name), cMuted(c.desc))
		}
	}

	fmt.Printf("\n%s %s %s\n\n", cDim("Run"), cWhite("ocm <command> --help"), cDim("for details on any command."))
}

// ── Styled token box ──

// printStyledTokenBox prints a bordered instruction box with color.
func printStyledTokenBox(tokenCmd string) {
	line1 := fmt.Sprintf("Run %s in another terminal.", cBold("`"+tokenCmd+"`"))
	line2 := "Then paste the generated token below."

	const w = 54
	bar := strings.Repeat("─", w)
	empty := fmt.Sprintf("  %s%s%s", cBox("│"), strings.Repeat(" ", w), cBox("│"))

	fmt.Printf("  %s\n", cBox("╭"+bar+"╮"))
	fmt.Println(empty)
	pad1 := w - 2 - visibleLen(line1)
	if pad1 < 0 {
		pad1 = 0
	}
	pad2 := w - 2 - visibleLen(line2)
	if pad2 < 0 {
		pad2 = 0
	}
	fmt.Printf("  %s  %s%s%s\n", cBox("│"), line1, strings.Repeat(" ", pad1), cBox("│"))
	fmt.Printf("  %s  %s%s%s\n", cBox("│"), line2, strings.Repeat(" ", pad2), cBox("│"))
	fmt.Println(empty)
	fmt.Printf("  %s\n", cBox("╰"+bar+"╯"))
}
