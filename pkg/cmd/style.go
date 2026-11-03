package cmd

import (
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

// style adds ANSI colors when output goes to a terminal and NO_COLOR is
// unset (https://no-color.org). Otherwise every method returns its input
// unchanged, so pipes, files and golden tests get plain text.
type style struct {
	on bool
}

func newStyle(w io.Writer) style {
	f, ok := w.(*os.File)
	return style{on: ok && os.Getenv("NO_COLOR") == "" && term.IsTerminal(int(f.Fd()))}
}

func (s style) wrap(code, text string) string {
	if !s.on || text == "" {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

func (s style) bold(t string) string   { return s.wrap("1", t) }
func (s style) dim(t string) string    { return s.wrap("2", t) }
func (s style) red(t string) string    { return s.wrap("31", t) }
func (s style) green(t string) string  { return s.wrap("32", t) }
func (s style) yellow(t string) string { return s.wrap("33", t) }

// padRight pads plain text to width in display columns. Pad before
// coloring: escape codes take no columns but count as bytes.
func padRight(text string, width int) string {
	if n := utf8.RuneCountInString(text); n < width {
		return text + strings.Repeat(" ", width-n)
	}
	return text
}

func padLeft(text string, width int) string {
	if n := utf8.RuneCountInString(text); n < width {
		return strings.Repeat(" ", width-n) + text
	}
	return text
}
