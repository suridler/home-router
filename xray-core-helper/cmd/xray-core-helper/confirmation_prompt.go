package main

import (
	"fmt"
	"io"
	"os"

	"github.com/mattn/go-isatty"
)

// The countdown occupies the row above the prompt. Updating it must leave
// the input row and cursor untouched, including text echoed by the terminal.
type confirmationPrompt struct {
	writer   io.Writer
	terminal bool
	started  bool
}

func confirmationTerminal(writer io.Writer) bool {
	f, ok := writer.(*os.File)
	return ok && os.Getenv("TERM") != "dumb" && isatty.IsTerminal(f.Fd())
}

func (prompt *confirmationPrompt) update(status string) {
	if !prompt.started {
		fmt.Fprintf(prompt.writer, "%s\nВыбор [4]: ", status)
		prompt.started = true
		return
	}
	if prompt.terminal {
		// Save/restore cursor in the same write; never redraw the input row.
		fmt.Fprintf(prompt.writer, "\x1b7\x1b[1A\r\x1b[2K%s\x1b8", status)
	}
}
