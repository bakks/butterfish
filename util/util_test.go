package util

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Sanity test
func TestStyleCodeblocksWriter(t *testing.T) {
	buffer := new(bytes.Buffer)
	writer := NewStyleCodeblocksWriter(buffer, 80, "", "", "")

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

func getStyleCodeblocksWriter() (*bytes.Buffer, *StyleCodeblocksWriter) {
	buffer := new(bytes.Buffer)
	return buffer, NewStyleCodeblocksWriter(buffer, 80, "NORMAL", "HIGHLIGHT", "")
}

func Test3BackticksNoLanguage(t *testing.T) {
	buffer, writer := getStyleCodeblocksWriter()

	testStr := `Hello
` + "```" + `
console.log('Hi');
` + "```" + `

Foo`

	writer.Write([]byte(testStr))

	expected := "Hello\n\rconsole.log('Hi');\r\x1b[38;5;231mconsole.log('Hi');\x1b[0m\nNORMAL\nFoo"

	// assert buffer equals expected
	assert.Equal(t, expected, buffer.String())
}

func Test3BackticksWithLanguage(t *testing.T) {
	buffer, writer := getStyleCodeblocksWriter()

	testStr := `Hello
` + "```javascript" + `
console.log(1);
` + "```" + `

Foo`

	writer.Write([]byte(testStr))

	expected := "Hello\n\rconsole.log(1);\r\x1b[38;5;148mconsole\x1b[0m\x1b[38;5;231m.\x1b[0m\x1b[38;5;148mlog\x1b[0m\x1b[38;5;231m(\x1b[0m\x1b[38;5;141m1\x1b[0m\x1b[38;5;231m);\x1b[0m\nNORMAL\nFoo"

	// assert buffer equals expected
	assert.Equal(t, expected, buffer.String())
}

func Test1BacktickInlined(t *testing.T) {
	buffer, writer := getStyleCodeblocksWriter()

	testStr := "Hello `console.log('Hi')` Foo"

	writer.Write([]byte(testStr))

	expected := "Hello HIGHLIGHTconsole.log('Hi')NORMAL Foo"

	// assert buffer equals expected
	assert.Equal(t, expected, buffer.String())
}

func Test1BacktickInlinedAtStartOfLine(t *testing.T) {
	buffer, writer := getStyleCodeblocksWriter()

	testStr := "`console.log('Hi')` Foo"

	writer.Write([]byte(testStr))

	expected := "HIGHLIGHTconsole.log('Hi')NORMAL Foo"

	// assert buffer equals expected
	assert.Equal(t, expected, buffer.String())
}

func Test3BackticksIndented(t *testing.T) {
	buffer, writer := getStyleCodeblocksWriter()

	testStr := `Hello

` + "   ```javascript" + `
   console.log(1);
` + "   ```" + `

Foo`

	writer.Write([]byte(testStr))

	expected := "Hello\n\n   \r   console.log(1);\r\x1b[38;5;231m   \x1b[0m\x1b[38;5;148mconsole\x1b[0m\x1b[38;5;231m.\x1b[0m\x1b[38;5;148mlog\x1b[0m\x1b[38;5;231m(\x1b[0m\x1b[38;5;141m1\x1b[0m\x1b[38;5;231m);\x1b[0m\n   NORMAL\nFoo"

	// assert buffer equals expected
	assert.Equal(t, expected, buffer.String())
}

func TestImageToBase64PNG(t *testing.T) {
	// Open test image
	file, err := os.Open("../assets/plugin.png")
	if err != nil {
		t.Fatalf("Failed to open test image: %v", err)
	}
	defer file.Close()

	// Convert to base64
	base64Str, err := ImageToBase64PNG(file)
	if err != nil {
		t.Fatalf("Failed to convert image to base64: %v", err)
	}

	// Basic validation
	if len(base64Str) == 0 {
		t.Error("Expected non-empty base64 string")
	}

	// Validate base64 format
	if !strings.HasPrefix(base64Str, "iVBOR") { // PNG files in base64 typically start with this
		t.Error("Expected base64 string to start with PNG header")
	}
}

func TestImageToBase64PNG_InvalidInput(t *testing.T) {
	// Test with invalid input
	invalidData := strings.NewReader("not an image")
	_, err := ImageToBase64PNG(invalidData)
	if err == nil {
		t.Error("Expected error for invalid image data")
	}
}

func TestCreateImageContent(t *testing.T) {
	// Open test image
	file, err := os.Open("../assets/plugin.png")
	if err != nil {
		t.Fatalf("Failed to open test image: %v", err)
	}
	defer file.Close()

	description := "Test plugin icon"
	imgContent, err := CreateImageContent(file, description)
	if err != nil {
		t.Fatalf("Failed to create image content: %v", err)
	}

	// Validate fields
	assert.Equal(t, "image/png", imgContent.MimeType)
	assert.Equal(t, description, imgContent.Description)
	assert.True(t, len(imgContent.Base64Content) > 0)
	assert.True(t, strings.HasPrefix(imgContent.Base64Content, "iVBOR")) // PNG header in base64
}

func TestCreateImageContent_InvalidInput(t *testing.T) {
	invalidData := strings.NewReader("not an image")
	_, err := CreateImageContent(invalidData, "invalid image")
	assert.Error(t, err)
}
