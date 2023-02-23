package util

type SetSizeMsg struct {
	Width  int
	Height int
}

func NewSetSizeMsg(width, height int) SetSizeMsg {
	return SetSizeMsg{Width: width, Height: height}
}
