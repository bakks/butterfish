package altscreenwrapper

import (
	"github.com/bakks/butterfish/bubbles/util"
	tea "github.com/charmbracelet/bubbletea"
)

// This is a simple wrapper for Bubble Tea AltScreen applications (where the
// entire console is used for the application). There's a small complication
// with AltScreen apps - when the terminal changes size they receive a
// WindowSizeMsg, but subcomponents may not know what their own size should be
// relative to the full terminal size. So I've standardized these components
// to receive a SetSizeMsg instead, which sets a uniform expectation and allows
// parent components to easily set child sizes.

type AltScreenWrapper struct {
	child tea.Model
}

func NewAltScreenWrapper(child tea.Model) AltScreenWrapper {
	return AltScreenWrapper{child}
}

func (this AltScreenWrapper) Init() tea.Cmd {
	return nil
}

func (this AltScreenWrapper) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	childMsg := msg

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// if this is is a WindowSizeMessage we turn it into a SetSizeMsg
		childMsg = util.SetSizeMsg{
			Width:  msg.Width,
			Height: msg.Height,
		}
	}

	var cmd tea.Cmd
	this.child, cmd = this.child.Update(childMsg)
	return this, cmd
}

func (this AltScreenWrapper) View() string {
	return this.child.View()
}
