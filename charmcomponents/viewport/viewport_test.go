package viewport

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestViewportBufferSimple(t *testing.T) {
	// Create a new viewportBuffer with a width of 10
	buf := newBuffer()
	buf.SetWidth(10)

	// Write a string to the buffer
	buf.WriteString("Hello\n")

	// Check that the string was written to the first line of the buffer
	assert.Equal(t, 2, buf.NumLines())
	assert.Equal(t, "Hello", buf.rawLines[0])
	assert.Equal(t, "", buf.rawLines[1])
	assert.Equal(t, 0, buf.lineIndex[0])
	assert.Equal(t, 1, buf.lineIndex[1])
	assert.Equal(t, "Hello", buf.wrappedLines[0])
	assert.Equal(t, "", buf.wrappedLines[1])

	buf.WriteString("Foo\n")
	assert.Equal(t, 3, buf.NumLines())
	assert.Equal(t, "Hello", buf.rawLines[0])
	assert.Equal(t, "Foo", buf.rawLines[1])
	assert.Equal(t, 0, buf.lineIndex[0])
	assert.Equal(t, 1, buf.lineIndex[1])
	assert.Equal(t, 2, buf.lineIndex[2])
	assert.Equal(t, "Hello", buf.wrappedLines[0])
	assert.Equal(t, "Foo", buf.wrappedLines[1])
}

// The viewport buffer receives writes, then produces an array of lines which
// can be used to render the viewport.
func TestViewportBufferBasic(t *testing.T) {
	// Create a new viewportBuffer with a width of 9
	buf := newBuffer()
	buf.SetWidth(9)

	// Write a string to the buffer
	buf.WriteString("Hello ")

	// Check that the string was written to the first line of the buffer
	lines := buf.Range(0, 1)
	assert.Equal(t, 1, buf.NumLines())
	assert.Equal(t, 1, len(lines))
	assert.Equal(t, "Hello ", lines[0])

	// Write another string to the buffer
	buf.WriteString("foo")
	assert.Equal(t, 1, buf.NumLines())
	lines = buf.Range(0, 1)
	assert.Equal(t, 1, len(lines))
	assert.Equal(t, "Hello foo", lines[0])

	// Write a string that will wrap to the next line
	buf.WriteString("world\nbar\n")
	lines = buf.Range(0, 4)
	assert.Equal(t, 4, buf.NumLines())
	assert.Equal(t, "Hello", lines[0])
	assert.Equal(t, "fooworld", lines[1])
	assert.Equal(t, "bar", lines[2])
	assert.Equal(t, "", lines[3])

}

func TestViewportBufferLongString(t *testing.T) {
	buf := newBuffer()
	buf.SetWidth(20)

	// Write a string to the buffer
	buf.WriteString("This is a long string that should wrap around to the next line\nfoo bar 1\nfoo bar 2\nfoo bar 3!")

	// Check that the string was written to the first line of the buffer
	assert.Equal(t, 7, buf.NumLines())
	lines := buf.Range(0, buf.NumLines())
	assert.Equal(t, "This is a long", lines[0])

	buf.WriteString("foo\n")
	assert.Equal(t, 8, buf.NumLines())
}

// Write 100 lines to a new buffer of width 20 then assert
// that the buffer is 100 lines long.
func TestViewportBufferManyWrites(t *testing.T) {
	buf := newBuffer()
	buf.SetWidth(20)

	for i := 0; i < 100; i++ {
		buf.WriteString(fmt.Sprintf("%d\n", i))
	}

	assert.Equal(t, 101, len(buf.rawLines))
	assert.Equal(t, 101, buf.NumLines())
}

func TestViewportScrolling(t *testing.T) {
	vp := New()
	vp.Width = 4
	vp.Height = 20
	vp.buffer.SetWidth(20)

	for i := 0; i < 100; i++ {
		vp.WriteString(fmt.Sprintf("%d\n", i))
	}

	out := vp.View()
	lines := strings.Split(out, "\n")

	assert.Equal(t, 20, len(lines))
	assert.Equal(t, "81  ", lines[0])
	assert.Equal(t, "99  ", lines[18])
	assert.Equal(t, "    ", lines[19])

	vp.GotoTop()
	out = vp.View()
	lines = strings.Split(out, "\n")
	assert.Equal(t, 20, len(lines))
	assert.Equal(t, "0   ", lines[0])

}
