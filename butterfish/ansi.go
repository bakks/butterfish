package butterfish

import "fmt"

func prettyAnsiCsi(data []byte) (int, string) {
	// eat digits
	i := 2
	for ; i < len(data); i++ {
		if data[i] < '0' || data[i] > '9' {
			break
		}
	}

	if i == 2 {
		return 2, "CSI"
		//panic(fmt.Sprintf("Unknown CSI sequence, expected digits: %x", data))
	}
	if i == len(data) {
		return i, "CSI"
		//panic(fmt.Sprintf("Unknown CSI sequence, expected command: %x", data))
	}

	switch data[i] {
	case 'A':
		return i + 1, "CUP"
	case 'B':
		return i + 1, "CUD"
	case 'C':
		return i + 1, "CUF"
	case 'D':
		return i + 1, "CUB"
	case 'E':
		return i + 1, "CNL"
	case 'F':
		return i + 1, "CPL"
	case 'J':
		return i + 1, "ED"
	case 'K':
		return i + 1, "EL"
	case 'S':
		return i + 1, "SU"
	case 'm':
		return i + 1, "SGR"
	case 'n':
		if data[2] == '6' {
			return i + 2, "DSR"
		}
		panic("Unknown CSI sequence")
	}

	panic("Unknown CSI sequence")
}

func prettyAnsiC1(data []byte) (int, string) {
	// C1 codes
	switch data[1] {
	case '\x5b', '\x9b':
		if len(data) >= 3 {
			return prettyAnsiCsi(data)
		}
		return 2, "CSI"
	case '\x8e':
		return 2, "SSA"
	case '\x8f':
		return 2, "ESA"
	case '\x90':
		return 2, "DCS"
	case '\x9c':
		return 2, "ST"
	case '\x9d':
		return 2, "OSC"
	case '\x98':
		return 2, "SOS"
	case '\x9e':
		return 2, "PM"
	case '\x9f':
		return 2, "APC"
	case 'Q':
		return 2, "PU1"
	case 'R':
		return 2, "PU2"
	}

	return 2, "C1"
	//panic(fmt.Sprintf("Unknown C1 sequence: %x", data))
}

// Given a byte array, check if the beginning of the byte array is an ANSI
// escape sequence, if so, return the length of that sequence and the
// abbreviation.
func prettyAnsi(data []byte) (int, string) {
	if data == nil || len(data) == 0 {
		return 0, ""
	}

	// C0 codes
	switch data[0] {
	case '\x00':
		return 1, "NUL"
	case '\x01':
		return 1, "SOH"
	case '\x02':
		return 1, "STX"
	case '\x03':
		return 1, "ETX"
	case '\x04':
		return 1, "EOT"
	case '\x05':
		return 1, "ENQ"
	case '\x06':
		return 1, "ACK"
	case '\x07':
		return 1, "BEL"
	case '\x08':
		return 1, "BS"
	case '\x09':
		return 1, "HT"
	case '\x0a':
		return 1, "LF"
	case '\x0b':
		return 1, "VT"
	case '\x0c':
		return 1, "FF"
	case '\x0d':
		return 1, "CR"
	case '\x0e':
		return 1, "SO"
	case '\x0f':
		return 1, "SI"
	case '\x10':
		return 1, "DLE"
	case '\x11':
		return 1, "DC1"
	case '\x12':
		return 1, "DC2"
	case '\x13':
		return 1, "DC3"
	case '\x14':
		return 1, "DC4"
	case '\x15':
		return 1, "NAK"
	case '\x16':
		return 1, "SYN"
	case '\x17':
		return 1, "ETB"
	case '\x18':
		return 1, "CAN"
	case '\x19':
		return 1, "EM"
	case '\x1a':
		return 1, "SUB"
	case '\x1b':
		if len(data) >= 2 {
			return prettyAnsiC1(data)
		}
		return 1, "ESC"
	case '\x1c':
		return 1, "FS"
	case '\x1d':
		return 1, "GS"
	case '\x1e':
		return 1, "RS"
	case '\x1f':
		return 1, "US"
	case '\x7f':
		return 1, "DEL"
	}

	return 0, ""
}

// Given a byte slice, return a string with 2 lines adjusted to the terminal
// width.
// First line: the bytes in hex
// Second line: the bytes in ascii, with ansi escape sequences described
// by their name/code
func prettyHex(data []byte, width int) string {
	hexLine := ""
	asciiLine := ""

	i := 0
	for i < len(data) {
		n, name := prettyAnsi(data[i:])
		if n > 0 {
			// we have an ansi escape code
			hexLine += fmt.Sprintf("%x ", data[i:i+n])
			asciiLine += fmt.Sprintf("%s ", name)
			i += n
		} else {
			hexLine += fmt.Sprintf("%02x ", data[i])
			asciiLine += fmt.Sprintf("%c ", data[i])
			i++
		}

		for len(hexLine) < len(asciiLine) {
			hexLine += " "
		}
		for len(asciiLine) < len(hexLine) {
			asciiLine += " "
		}

	}

	return hexLine + "\n" + asciiLine
}
