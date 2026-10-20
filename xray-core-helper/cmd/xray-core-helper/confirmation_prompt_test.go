package main

import (
	"strings"
	"testing"
)

func TestConfirmationPromptKeepsInputRowDuringCountdown(t *testing.T) {
	var out strings.Builder
	prompt := confirmationPrompt{writer: &out, terminal: true}
	prompt.update("Осталось 01:20")
	if out.String() != "Осталось 01:20\nВыбор [4]: " {
		t.Fatal(out.String())
	}
	out.Reset()
	prompt.update("Осталось 01:19")
	if out.String() != "\x1b7\x1b[1A\r\x1b[2KОсталось 01:19\x1b8" {
		t.Fatalf("update must save cursor, change only preceding row and restore cursor: %q", out.String())
	}
	prompt.started = false // Input was submitted; a new prompt follows probe/errors.
	out.Reset()
	prompt.update("Осталось 01:10")
	if out.String() != "Осталось 01:10\nВыбор [4]: " {
		t.Fatal(out.String())
	}
}

func TestConfirmationPromptDoesNotRedrawRedirectedOutput(t *testing.T) {
	var out strings.Builder
	if confirmationTerminal(&out) {
		t.Fatal("buffer is not a terminal")
	}
	prompt := confirmationPrompt{writer: &out}
	prompt.update("Осталось 01:20")
	out.Reset()
	prompt.update("Осталось 01:19")
	if out.Len() != 0 {
		t.Fatal("redirected output must not receive cursor commands or repeated prompts", out.String())
	}
}
