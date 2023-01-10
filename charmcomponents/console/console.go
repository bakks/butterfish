package console

import (
	"fmt"
	"log"

	alt "github.com/bakks/teglon/butterfish/charmcomponents/altscreenwrapper"
	"github.com/bakks/teglon/butterfish/charmcomponents/util"
	"github.com/bakks/teglon/butterfish/charmcomponents/viewport"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// This is a Charm BubbleTea model that proides a console
// with a prompt and a viewport for displaying output.
// We provide a ConsoleProgram wrapper that provides:
// - A callback for when the user enters a command
// - A callback for when the program exits
// - An io.Writer implementation for printing to the console

type ConsolePrintMsg struct {
	Text string
}

func (this *ConsoleProgram) Write(p []byte) (n int, err error) {
	this.program.Send(ConsolePrintMsg{Text: string(p)})
	return len(p), nil
}

func (this *ConsoleProgram) Printf(format string, args ...any) {
	fmt.Fprintf(this, format, args...)
}

func (this *ConsoleProgram) Println(format string) {
	fmt.Fprintf(this, "%s\n", format)
}

type ConsoleProgram struct {
	program *tea.Program
}

func NewConsoleProgram(commandCallback func(string), exitCallback func()) *ConsoleProgram {
	consoleModel := NewConsoleModel(commandCallback)
	wrapper := alt.NewAltScreenWrapper(consoleModel)
	program := tea.NewProgram(wrapper, tea.WithAltScreen())

	go func() {
		_, err := program.Run()

		if err != nil {
			log.Fatal(err)
		}

		exitCallback()
	}()

	return &ConsoleProgram{
		program: program,
	}
}

type ConsoleModel struct {
	width           int
	height          int
	viewport        viewport.Model
	textarea        textarea.Model
	promptOutStyle  lipgloss.Style
	promptTextStyle lipgloss.Style
	err             error
	commandCallback func(string)
}

func NewConsoleModel(callback func(string)) ConsoleModel {
	ta := textarea.New()
	ta.Placeholder = "Enter GPT prompt..."
	ta.Focus()

	ta.Prompt = "┃ "
	ta.CharLimit = 280

	// Remove cursor line styling
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.ShowLineNumbers = false

	vp := viewport.New()

	ta.KeyMap.InsertNewline.SetEnabled(false)

	return ConsoleModel{
		width:           20,
		height:          20,
		textarea:        ta,
		viewport:        vp,
		promptOutStyle:  lipgloss.NewStyle().Foreground(lipgloss.Color("5")),
		promptTextStyle: lipgloss.NewStyle().Foreground(lipgloss.Color("12")),
		err:             nil,
		commandCallback: callback,
	}
}

func (this ConsoleModel) Init() tea.Cmd {
	return textarea.Blink
}

func consoleChildSizes(width, height int) (int, int, int, int) {
	taWidth := width
	taHeight := 3
	vpWidth := width
	vpHeight := height - taHeight

	return vpWidth, vpHeight, taWidth, taHeight
}

func (this ConsoleModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var taCmd tea.Cmd
	var vpCmd tea.Cmd
	passMsgOn := true

	switch msg := msg.(type) {
	case util.SetSizeMsg:
		passMsgOn = false
		this.width = msg.Width
		this.height = msg.Height

		vpWidth, vpHeight, taWidth, taHeight := consoleChildSizes(this.width, this.height)
		vpMsg := util.NewSetSizeMsg(vpWidth, vpHeight)
		this.viewport, vpCmd = this.viewport.Update(vpMsg)

		this.textarea.SetWidth(taWidth)
		this.textarea.SetHeight(taHeight)

	case ConsolePrintMsg:
		this.viewport.WriteString(msg.Text)
		this.viewport.GotoBottom()

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			fmt.Println(this.textarea.Value())
			return this, tea.Quit

		case tea.KeyEnter:
			cmd := this.textarea.Value()
			newLine := fmt.Sprintf("\n\n%s %s\n", this.promptOutStyle.Render(">"), this.promptTextStyle.Render(cmd))

			this.viewport.WriteString(newLine)
			this.textarea.Reset()
			this.viewport.GotoBottom()
			this.commandCallback(cmd)
		}

	// We handle errors just like any other message
	case error:
		this.err = msg
		return this, nil
	}

	if passMsgOn {
		this.viewport, vpCmd = this.viewport.Update(msg)
		this.textarea, taCmd = this.textarea.Update(msg)
	}

	return this, tea.Batch(taCmd, vpCmd)
}

func (this ConsoleModel) View() string {
	return fmt.Sprintf(
		"%s\n%s",
		this.viewport.View(),
		this.textarea.View(),
	)
}
