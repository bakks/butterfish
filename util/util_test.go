package util

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Sanity test
func TestStyleCodeblocksWriter(t *testing.T) {
	buffer := new(bytes.Buffer)
	writer := NewStyleCodeblocksWriter(buffer, 80, "", "")

	writer.Write([]byte("Hello\n"))
	writer.Write([]byte("```javascript\n"))
	writer.Write([]byte("console.log('Hi');\n"))
	writer.Write([]byte("```\n"))
	writer.Write([]byte("Foo\n"))

	// buffer should contain Hi
	if !strings.Contains(buffer.String(), "Hi") {
		t.Error("buffer should contain hi")
		fmt.Println(buffer.String())
	}

	buffer.Reset()
	writer.Write([]byte("`"))
	writer.Write([]byte("`"))
	writer.Write([]byte("`\n"))
	writer.Write([]byte("console.l"))
	writer.Write([]byte("og('blah');\n"))
	writer.Write([]byte("```\n"))

	if !strings.Contains(buffer.String(), "blah") {
		t.Error("buffer should contain blah")
		fmt.Println(buffer.String())
	}
}

// We want to now test several patterns:
// 1. 3 backticks, no language
// 2. 3 backticks, with language
// 3. 1 backtick inlined
// 4. 1 backtick inlined, at start of line
// 5. 3 backticks, indented

func Test3BackticksNoLanguage(t *testing.T) {
	buffer := new(bytes.Buffer)
	writer := NewStyleCodeblocksWriter(buffer, 80, "NORMAL", "HIGHLIGHT")

	testStr := `Hello
` + "```" + `
console.log('Hi');
` + "```" + `

Foo`

	writer.Write([]byte(testStr))

	expected := "Hello\n\nconsole.log('Hi');\r\x1b[38;5;231mconsole.log('Hi');\x1b[0m\nNORMAL\nFoo"

	// assert buffer equals expected
	assert.Equal(t, expected, buffer.String())
}

func Test3BackticksWithLanguage(t *testing.T) {
	buffer := new(bytes.Buffer)
	writer := NewStyleCodeblocksWriter(buffer, 80, "NORMAL", "HIGHLIGHT")

	testStr := `Hello
` + "```javascript" + `
console.log(1);
` + "```" + `

Foo`

	writer.Write([]byte(testStr))

	expected := "Hello\n\nconsole.log(1);\r\x1b[38;5;148mconsole\x1b[0m\x1b[38;5;231m.\x1b[0m\x1b[38;5;148mlog\x1b[0m\x1b[38;5;231m(\x1b[0m\x1b[38;5;141m1\x1b[0m\x1b[38;5;231m);\x1b[0m\nNORMAL\nFoo"

	// assert buffer equals expected
	assert.Equal(t, expected, buffer.String())
}

func Test1BacktickInlined(t *testing.T) {
	buffer := new(bytes.Buffer)
	writer := NewStyleCodeblocksWriter(buffer, 80, "NORMAL", "HIGHLIGHT")

	testStr := "Hello `console.log('Hi')` Foo"

	writer.Write([]byte(testStr))

	expected := "Hello HIGHLIGHTconsole.log('Hi')NORMAL Foo"

	// assert buffer equals expected
	assert.Equal(t, expected, buffer.String())
}

func Test1BacktickInlinedAtStartOfLine(t *testing.T) {
	buffer := new(bytes.Buffer)
	writer := NewStyleCodeblocksWriter(buffer, 80, "NORMAL", "HIGHLIGHT")

	testStr := "`console.log('Hi')` Foo"

	writer.Write([]byte(testStr))

	expected := "HIGHLIGHTconsole.log('Hi')NORMAL Foo"

	// assert buffer equals expected
	assert.Equal(t, expected, buffer.String())
}

func Test3BackticksIndented(t *testing.T) {
	buffer := new(bytes.Buffer)
	writer := NewStyleCodeblocksWriter(buffer, 80, "NORMAL", "HIGHLIGHT")

	testStr := `Hello

` + "   ```javascript" + `
   console.log(1);
` + "   ```" + `

Foo`

	writer.Write([]byte(testStr))

	expected := "Hello\n\n   \n   console.log(1);\r\x1b[38;5;231m   \x1b[0m\x1b[38;5;148mconsole\x1b[0m\x1b[38;5;231m.\x1b[0m\x1b[38;5;148mlog\x1b[0m\x1b[38;5;231m(\x1b[0m\x1b[38;5;141m1\x1b[0m\x1b[38;5;231m);\x1b[0m\n   NORMAL\nFoo"

	// assert buffer equals expected
	assert.Equal(t, expected, buffer.String())
}
