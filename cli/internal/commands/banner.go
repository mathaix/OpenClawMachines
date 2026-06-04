package commands

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

// prize characters — using ASCII-safe symbols that are always 1 column wide
var prizes = []rune{'*', 'o', '+', 'x', '@', '#', 'o', '*', '+', '@', 'x', '#', '*', 'o', '+', 'x'}

// getTermWidth returns the terminal width, trying multiple methods.
func getTermWidth() int {
	// Try all three standard fds
	for _, fd := range []int{int(os.Stdout.Fd()), int(os.Stderr.Fd()), int(os.Stdin.Fd())} {
		if w, _, err := term.GetSize(fd); err == nil && w > 0 {
			return w
		}
	}
	// Try stty size (works on macOS/Linux even when Go can't get it)
	if out, err := exec.Command("stty", "size").Output(); err == nil {
		parts := strings.Fields(strings.TrimSpace(string(out)))
		if len(parts) == 2 {
			if w, err := strconv.Atoi(parts[1]); err == nil && w > 0 {
				return w
			}
		}
	}
	// Try tput cols
	if out, err := exec.Command("tput", "cols").Output(); err == nil {
		if w, err := strconv.Atoi(strings.TrimSpace(string(out))); err == nil && w > 0 {
			return w
		}
	}
	// Fall back to $COLUMNS env var
	if cols := os.Getenv("COLUMNS"); cols != "" {
		if w, err := strconv.Atoi(cols); err == nil && w > 0 {
			return w
		}
	}
	return 80
}

// prizeRow builds a row of scattered prize symbols to fill the width.
func prizeRow(inner int, offset int) string {
	buf := make([]rune, inner)
	for i := range buf {
		buf[i] = ' '
	}
	idx := offset
	for col := 1; col < inner-1; col += 3 {
		buf[col] = prizes[idx%len(prizes)]
		idx++
	}
	return string(buf)
}

// buildFrame generates a single animation frame at the given terminal width.
func buildFrame(w int, clawRow int, grabbed bool) string {
	inner := w - 2 // space between the two wall characters

	hBar := strings.Repeat("=", inner)
	topBar := "+" + hBar + "+"
	midBar := "+" + hBar + "+"
	botBar := "+" + hBar + "+"

	title1 := "O P E N C L A W"
	title2 := "M A C H I N E S"

	var lines []string
	lines = append(lines, topBar)
	lines = append(lines, "|"+centerPad(title1, inner)+"|")
	lines = append(lines, "|"+centerPad(title2, inner)+"|")
	lines = append(lines, midBar)

	// Interior: 7 rows (indices 0-6)
	// Rows 5-6 are prize rows, claw can be at rows 0-4
	interiorRows := 7
	clawCol := inner / 2

	for row := 0; row < interiorRows; row++ {
		buf := make([]rune, inner)
		for i := range buf {
			buf[i] = ' '
		}

		if row < 5 {
			if row < clawRow {
				// Cable above claw
				if clawCol < inner {
					buf[clawCol] = '|'
				}
			} else if row == clawRow {
				if grabbed {
					// Closed claw with prize
					if clawCol-1 >= 0 && clawCol+1 < inner {
						buf[clawCol-1] = '\\'
						buf[clawCol] = '@'
						buf[clawCol+1] = '/'
					}
				} else {
					// Claw connector
					if clawCol-1 >= 0 && clawCol+1 < inner {
						buf[clawCol-1] = '['
						buf[clawCol] = '+'
						buf[clawCol+1] = ']'
					}
				}
			} else if row == clawRow+1 && !grabbed {
				// Open claw arms
				if clawCol-2 >= 0 && clawCol+2 < inner {
					buf[clawCol-2] = '/'
					buf[clawCol+2] = '\\'
				}
			}
			lines = append(lines, "|"+string(buf)+"|")
		} else {
			// Prize rows
			offset := 0
			if row == 6 {
				offset = 2
			}
			pr := []rune(prizeRow(inner, offset))

			// Remove one prize near the claw if grabbed at prize level
			if grabbed && clawRow == 4 && row == 5 {
				for i := clawCol - 2; i <= clawCol+2 && i < len(pr); i++ {
					if i >= 0 && pr[i] != ' ' {
						pr[i] = ' '
						break
					}
				}
			}

			lines = append(lines, "|"+string(pr)+"|")
		}
	}

	lines = append(lines, botBar)
	return strings.Join(lines, "\n")
}

// centerPad centers text in exactly `width` characters.
func centerPad(text string, width int) string {
	tl := len(text) // ASCII-safe, no multi-byte
	if tl >= width {
		return text[:width]
	}
	left := (width - tl) / 2
	right := width - tl - left
	return strings.Repeat(" ", left) + text + strings.Repeat(" ", right)
}

// playBanner shows the animated claw machine banner.
func playBanner(w io.Writer) {
	if !isTerminal() {
		return
	}

	width := getTermWidth()
	if width < 40 {
		width = 40
	}

	type frame struct {
		clawRow int
		grabbed bool
		delay   time.Duration
	}

	frames := []frame{
		{0, false, 400 * time.Millisecond}, // claw at top, open
		{1, false, 100 * time.Millisecond}, // descending
		{2, false, 100 * time.Millisecond}, // descending
		{3, false, 100 * time.Millisecond}, // descending
		{4, false, 200 * time.Millisecond}, // at prize level, open
		{4, true, 300 * time.Millisecond},  // grab!
		{3, true, 100 * time.Millisecond},  // rising
		{2, true, 100 * time.Millisecond},  // rising
		{1, true, 100 * time.Millisecond},  // rising
		{0, true, 600 * time.Millisecond},  // at top with prize
	}

	firstFrame := buildFrame(width, frames[0].clawRow, frames[0].grabbed)
	lineCount := strings.Count(firstFrame, "\n") + 1

	for i, f := range frames {
		if i > 0 {
			fmt.Fprintf(w, "\033[%dA", lineCount)
		}
		fmt.Fprintln(w, buildFrame(width, f.clawRow, f.grabbed))
		time.Sleep(f.delay)
	}

	fmt.Println()
}

// isTerminal returns true if stdout is a terminal (not piped/redirected).
func isTerminal() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}
