package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-tailscale/internal/headscale"
	"github.com/tui-tools/tui-tailscale/internal/tailscale"
)

// newCPApp builds an app around the two demo backends with their state
// already loaded, so a control-plane test can drive it without the tea
// runtime.
func newCPApp(t *testing.T) *app {
	t.Helper()
	a := newApp(tailscale.NewFake(), headscale.NewFake(), theme.New(), nil)
	a.width, a.height = 100, 30
	a.files = demoFiles()
	a.Update(a.load()())
	a.loading = false
	return a
}

func key(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// confirmAndRun accepts the open confirm dialog and feeds the resulting
// command's message back into the model, the way the tea runtime would.
func confirmAndRun(t *testing.T, a *app) *app {
	t.Helper()
	if a.mode != modeConfirm {
		t.Fatalf("no confirm dialog is open (mode %d)", a.mode)
	}
	model, cmd := a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	a = model.(*app)
	if cmd == nil {
		t.Fatal("confirming produced no command")
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			model, _ = a.Update(c())
			a = model.(*app)
		}
		return a
	}
	model, _ = a.Update(msg)
	return model.(*app)
}

// typeAndEnter types a line into the open input and submits it.
func typeAndEnter(t *testing.T, a *app, text string) *app {
	t.Helper()
	if a.mode != modeInput {
		t.Fatalf("no input is open (mode %d)", a.mode)
	}
	model, _ := a.Update(key(text))
	a = model.(*app)
	model, _ = a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return model.(*app)
}

// pasteFile pastes a path into the open file picker and submits it, the way a
// path copied from another terminal is used.
func pasteFile(t *testing.T, a *app, path string) (*app, tea.Cmd) {
	t.Helper()
	if a.mode != modeFilePicker {
		t.Fatalf("no file picker is open (mode %d, status %q)", a.mode, a.status)
	}
	if a.filePicker.Typing() {
		// A refused path stays in the field; esc goes back to the list, so
		// the paste replaces it rather than adding to it.
		model, _ := a.Update(tea.KeyMsg{Type: tea.KeyEsc})
		a = model.(*app)
	}
	model, _ := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(path), Paste: true})
	a = model.(*app)
	model, cmd := a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return model.(*app), cmd
}
