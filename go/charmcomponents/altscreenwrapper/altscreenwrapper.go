package altscreenwrapper

import (
	"github.com/bakks/butterfish/go/charmcomponents/util"
	tea "github.com/charmbracelet/bubbletea"
)

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
