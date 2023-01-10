package viewport

import (
	"log"
	"math"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/wordwrap"
	"github.com/muesli/reflow/wrap"

	"github.com/bakks/butterfish/go/charmcomponents/util"
)

// A reimplementation of the Bubbles Viewport to behave more like a console
// output. This was necessary because the existing Viewport
// 1. Doesn't seem to handle wrapping
// 2. Doesn't handle resizing
// 3. Replaces the entire viewport buffer on every update

// KeyMap defines the keybindings for the viewport. Note that you don't
// necessary need to use keybindings at all; the viewport can be controlled
// programmatically with methods like Model.LineDown(1). See the GoDocs for
// details.
type KeyMap struct {
	PageDown     key.Binding
	PageUp       key.Binding
	HalfPageUp   key.Binding
	HalfPageDown key.Binding
	Down         key.Binding
	Up           key.Binding
}

// DefaultKeyMap returns a set of pager-like default keybindings.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		PageDown: key.NewBinding(
			key.WithKeys("pgdown"),
			key.WithHelp("pgdn", "page down"),
		),
		PageUp: key.NewBinding(
			key.WithKeys("pgup"),
			key.WithHelp("pgup", "page up"),
		),
	}
}

// viewportBuffer manages the underlying text of a viewport.
// This supports:
//   - Wrapping text written to the buffer
//   - Reading ranges of lines with O(1) complexity
//   - Resizing the wrapping width at O(n) complexity, n == number of lines
// Internals
//   - rawLines is the raw text written to the buffer
//   - lineIndex is a mapping of rawLines to wrappedLines
//   - wrappedLines is the wrapped text and what is actually displayed
type viewportBuffer struct {
	width        int
	rawLines     []string
	lineIndex    []int
	wrappedLines []string
}

func newBuffer() *viewportBuffer {
	return &viewportBuffer{
		rawLines:     make([]string, 0, 1024),
		lineIndex:    make([]int, 0, 1024),
		wrappedLines: make([]string, 0, 1024),
	}
}

func (this *viewportBuffer) Write(p []byte) (n int, err error) {
	this.WriteString(string(p))
	return len(p), nil
}

func (this *viewportBuffer) wrap(s string) string {
	wrapper := wrap.NewWriter(this.width)
	wrapper.PreserveSpace = true
	wordWrapped := wordwrap.String(s, this.width)
	wrapper.Write([]byte(wordWrapped))
	wrapped := wrapper.String()

	return wrapped
}

// Expand slice X until it is of size l, if less than length l,
// return expanded slice
func setSliceLen[S any](x []S, l int) []S {
	if len(x) < l {
		x = append(x, make([]S, l-len(x))...)
	} else if len(x) > l {
		x = x[:l]
	}
	return x
}

func setSliceMinLen[S any](x []S, l int) []S {
	if len(x) < l {
		x = append(x, make([]S, l-len(x))...)
	}
	return x
}

// Starting from a specific index, we update lineIndex and wrappedLines
// to reflect the content of rawLines, iterating to the end of rawLines
// and expanding the other slices as necessary.
func (this *viewportBuffer) wrapRange(start int) {
	wrapIndex := 0

	if start > 0 {
		wrapIndex = this.lineIndex[start]
	}

	this.lineIndex = setSliceLen(this.lineIndex, len(this.rawLines))

	for i, line := range this.rawLines[start:] {
		var wrappedLines []string

		if len(line) > this.width {
			w := this.wrap(line)
			wrappedLines = strings.Split(w, "\n")
		} else {
			wrappedLines = []string{line}
		}

		this.lineIndex[i+start] = wrapIndex

		this.wrappedLines = setSliceMinLen(this.wrappedLines, wrapIndex+len(wrappedLines))
		for _, wrappedLine := range wrappedLines {
			this.wrappedLines[wrapIndex] = wrappedLine
			wrapIndex++
		}
	}

	// we've gone to the end of rawLines so drop any excess wrapped lines
	this.wrappedLines = setSliceLen(this.wrappedLines, wrapIndex)
}

func (this *viewportBuffer) WriteString(s string) {
	lineStart := ""
	l := len(this.rawLines)
	// split the new content into lines
	lines := strings.Split(s, "\n")

	// if we haven't written any content then just assign the lines
	if l == 0 {
		this.rawLines = lines
		this.wrapRange(0)
		return
	}

	// if we have an unfinished line then we append to that line
	lineStart = this.rawLines[l-1]
	lines[0] = lineStart + lines[0]

	// update rawLines
	this.rawLines = append(this.rawLines[:l-1], lines...)

	// turn the new raw lines into wrapped lines
	this.wrapRange(l - 1)
}

func (this *viewportBuffer) SetWidth(width int) {
	this.width = width
	// rewrap everything from the raw lines
	this.wrapRange(0)
}

func (this *viewportBuffer) NumLines() int {
	return len(this.wrappedLines)
}

func (this *viewportBuffer) Range(start, end int) []string {
	l := len(this.wrappedLines)
	if (start < 0) || (end < 0) || (start > l+1) || (end > l+1) {
		log.Fatalf("Invalid range: %d-%d, length is %d", start, end, l)
		return nil
	}
	return this.wrappedLines[start:end]
}

// New returns a new model with the given width and height as well as default
// key mappings.
func New() (this Model) {
	this.buffer = newBuffer()
	this.setInitialValues()
	return this
}

// Model is the Bubble Tea model for this viewport element.
type Model struct {
	Width  int
	Height int
	KeyMap KeyMap

	// Whether or not to respond to the mouse. The mouse must be enabled in
	// Bubble Tea for this to work. For details, see the Bubble Tea docs.
	MouseWheelEnabled bool

	// The number of lines the mouse wheel will scroll. By default, this is 3.
	MouseWheelDelta int

	// YOffset is the vertical scroll position.
	YOffset int

	// YPosition is the position of the viewport in relation to the terminal
	// window. It's used in high performance rendering only.
	YPosition int

	// Style applies a lipgloss style to the viewport. Realistically, it's most
	// useful for setting borders, margins and padding.
	Style lipgloss.Style

	initialized bool
	buffer      *viewportBuffer
}

func (this *Model) WriteString(s string) {
	this.buffer.WriteString(s)
	this.GotoBottom()
}

func (this *Model) Write(p []byte) (n int, err error) {
	a, b := this.buffer.Write(p)
	this.GotoBottom()
	return a, b
}

func (this *Model) setInitialValues() {
	this.KeyMap = DefaultKeyMap()
	this.MouseWheelEnabled = true
	this.MouseWheelDelta = 3
	this.Width = 80
	this.Height = 20
	this.initialized = true
}

// Init exists to satisfy the tea.Model interface for composability purposes.
func (this Model) Init() tea.Cmd {
	return nil
}

// AtTop returns whether or not the viewport is in the very top position.
func (this Model) AtTop() bool {
	return this.YOffset <= 0
}

// AtBottom returns whether or not the viewport is at or past the very bottom
// position.
func (this Model) AtBottom() bool {
	return this.YOffset >= this.maxYOffset()
}

// PastBottom returns whether or not the viewport is scrolled beyond the last
// line. This can happen when adjusting the viewport height.
func (this Model) PastBottom() bool {
	return this.YOffset > this.maxYOffset()
}

// ScrollPercent returns the amount scrolled as a float between 0 and 1.
func (this Model) ScrollPercent() float64 {
	if this.Height >= this.buffer.NumLines() {
		return 1.0
	}
	y := float64(this.YOffset)
	h := float64(this.Height)
	t := float64(this.buffer.NumLines() - 1)
	v := y / (t - h)
	return math.Max(0.0, math.Min(1.0, v))
}

// maxYOffset returns the maximum possible value of the y-offset based on the
// viewport's content and set height.
func (this Model) maxYOffset() int {
	return max(0, this.buffer.NumLines()-this.Height)
}

// visibleLines returns the lines that should currently be visible in the
// viewport.
func (this Model) visibleLines() (lines []string) {
	numLines := this.buffer.NumLines()

	if numLines > 0 {
		top := max(0, this.YOffset)
		bottom := clamp(this.YOffset+this.Height, top, numLines)
		lines = this.buffer.Range(top, bottom)
	}
	return lines
}

// scrollArea returns the scrollable boundaries for high performance rendering.
func (this Model) scrollArea() (top, bottom int) {
	top = max(0, this.YPosition)
	bottom = max(top, top+this.Height)
	if top > 0 && bottom > top {
		bottom--
	}
	return top, bottom
}

// SetYOffset sets the Y offset.
func (this *Model) SetYOffset(n int) {
	this.YOffset = clamp(n, 0, this.maxYOffset())
}

// ViewDown moves the view down by the number of lines in the viewport.
// Basically, "page down".
func (this *Model) ViewDown() []string {
	if this.AtBottom() {
		return nil
	}

	this.SetYOffset(this.YOffset + this.Height)
	return this.visibleLines()
}

// ViewUp moves the view up by one height of the viewport. Basically, "page up".
func (this *Model) ViewUp() []string {
	if this.AtTop() {
		return nil
	}

	this.SetYOffset(this.YOffset - this.Height)
	return this.visibleLines()
}

// HalfViewDown moves the view down by half the height of the viewport.
func (m *Model) HalfViewDown() (lines []string) {
	if m.AtBottom() {
		return nil
	}

	m.SetYOffset(m.YOffset + m.Height/2)
	return m.visibleLines()
}

// HalfViewUp moves the view up by half the height of the viewport.
func (m *Model) HalfViewUp() (lines []string) {
	if m.AtTop() {
		return nil
	}

	m.SetYOffset(m.YOffset - m.Height/2)
	return m.visibleLines()
}

// LineDown moves the view down by the given number of lines.
func (m *Model) LineDown(n int) (lines []string) {
	if m.AtBottom() || n == 0 {
		return nil
	}

	// Make sure the number of lines by which we're going to scroll isn't
	// greater than the number of lines we actually have left before we reach
	// the bottom.
	m.SetYOffset(m.YOffset + n)
	return m.visibleLines()
}

// LineUp moves the view down by the given number of lines. Returns the new
// lines to show.
func (m *Model) LineUp(n int) (lines []string) {
	if m.AtTop() || n == 0 {
		return nil
	}

	// Make sure the number of lines by which we're going to scroll isn't
	// greater than the number of lines we are from the top.
	m.SetYOffset(m.YOffset - n)
	return m.visibleLines()
}

// GotoTop sets the viewport to the top position.
func (m *Model) GotoTop() (lines []string) {
	if m.AtTop() {
		return nil
	}

	m.SetYOffset(0)
	return m.visibleLines()
}

// GotoBottom sets the viewport to the bottom position.
func (m *Model) GotoBottom() (lines []string) {
	m.SetYOffset(m.maxYOffset())
	return m.visibleLines()
}

// ViewDown is a high performance command that moves the viewport up by a given
// number of lines. Use Model.ViewDown to get the lines that should be rendered.
// For example:
//
//     lines := model.ViewDown(1)
//     cmd := ViewDown(m, lines)
//
func ViewDown(m Model, lines []string) tea.Cmd {
	if len(lines) == 0 {
		return nil
	}
	top, bottom := m.scrollArea()
	return tea.ScrollDown(lines, top, bottom)
}

// ViewUp is a high performance command the moves the viewport down by a given
// number of lines height. Use Model.ViewUp to get the lines that should be
// rendered.
func ViewUp(m Model, lines []string) tea.Cmd {
	if len(lines) == 0 {
		return nil
	}
	top, bottom := m.scrollArea()
	return tea.ScrollUp(lines, top, bottom)
}

// Update handles standard message-based viewport updates.
func (this Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if !this.initialized {
		this.setInitialValues()
	}

	var cmd tea.Cmd

	switch msg := msg.(type) {
	case util.SetSizeMsg:
		this.Height = msg.Height
		this.Width = msg.Width
		// this is a potentially expensive call
		this.buffer.SetWidth(msg.Width)

	case tea.KeyMsg:
		switch {
		case key.Matches(msg, this.KeyMap.PageDown):
			this.ViewDown()

		case key.Matches(msg, this.KeyMap.PageUp):
			this.ViewUp()

		case key.Matches(msg, this.KeyMap.HalfPageDown):
			this.HalfViewDown()

		case key.Matches(msg, this.KeyMap.HalfPageUp):
			this.HalfViewUp()

		case key.Matches(msg, this.KeyMap.Down):
			this.LineDown(1)

		case key.Matches(msg, this.KeyMap.Up):
			this.LineUp(1)
		}

	case tea.MouseMsg:
		if !this.MouseWheelEnabled {
			break
		}
		switch msg.Type {
		case tea.MouseWheelUp:
			this.LineUp(this.MouseWheelDelta)

		case tea.MouseWheelDown:
			this.LineDown(this.MouseWheelDelta)
		}
	}

	return this, cmd
}

// View renders the viewport into a string.
func (this Model) View() string {
	content := strings.Join(this.visibleLines(), "\n")
	rendered := lipgloss.NewStyle().
		Width(this.Width).
		Height(this.Height).    // pad to height.
		MaxHeight(this.Height). // truncate height if taller.
		MaxWidth(this.Width).   // truncate width.
		Render(content)
	return rendered
}

func clamp(v, low, high int) int {
	if high < low {
		low, high = high, low
	}
	return min(high, max(low, v))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
