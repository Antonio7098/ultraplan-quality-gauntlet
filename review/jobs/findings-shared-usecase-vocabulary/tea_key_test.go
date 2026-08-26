package tui

import tea "github.com/charmbracelet/bubbletea"

func teaKey(k string) tea.Msg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)} }
