package butterfish

import (
	"fmt"
	"unicode/utf8"

	"github.com/bakks/butterfish/util"
)

// The Responses API validates each shell output stdout/stderr string against
// this per-field limit.
const responsesAPIStringMaxLength = 10 * 1024 * 1024

func shellOutputStringLimit(maxOutputLength int64) int {
	if maxOutputLength > 0 && maxOutputLength < int64(responsesAPIStringMaxLength) {
		return int(maxOutputLength)
	}
	return responsesAPIStringMaxLength
}

func suffixWithinBytes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}

	start := len(s) - limit
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

func truncateShellOutputString(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}

	omitted := len(s) - limit
	for {
		prefix := fmt.Sprintf("\n\n[butterfish truncated %d bytes from the beginning of this shell output]\n", omitted)
		if len(prefix) >= limit {
			return suffixWithinBytes(prefix, limit)
		}

		tailLimit := limit - len(prefix)
		nextOmitted := len(s) - tailLimit
		if nextOmitted == omitted {
			return prefix + suffixWithinBytes(s, tailLimit)
		}
		omitted = nextOmitted
	}
}

func truncateShellCallOutputForResponses(output *util.ShellCallOutput) *util.ShellCallOutput {
	if output == nil {
		return nil
	}

	limit := shellOutputStringLimit(output.MaxOutputLength)
	truncated := *output
	truncated.Output = make([]util.ShellCallOutputItem, len(output.Output))
	for i, item := range output.Output {
		item.Stdout = truncateShellOutputString(item.Stdout, limit)
		item.Stderr = truncateShellOutputString(item.Stderr, limit)
		truncated.Output[i] = item
	}
	return &truncated
}
