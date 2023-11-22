package util

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestStyleCodeblocksWriter(t *testing.T) {
	buffer := new(bytes.Buffer)
	writer := NewStyleCodeblocksWriter(buffer, 80, "")

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
