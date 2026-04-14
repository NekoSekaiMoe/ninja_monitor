// Copyright 2017 Google Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package terminal

import (
	"bytes"
	"io"
	"os"
	"syscall"
	"unsafe"
)

// IsSmartTerminal determines whether the given writer is a "smart" terminal.
// A smart terminal is one that typically supports advanced features such as
// color output, cursor control, and other ANSI escape sequence capabilities.
// This is in contrast to "dumb" terminals or non-terminal devices (like files).
//
// Parameter w: An io.Writer interface representing the output destination
// Return value: Returns true if the writer is a smart terminal, false otherwise
func IsSmartTerminal(w io.Writer) bool {
	return isSmartTerminal(w)
}

// isSmartTerminal is the internal implementation of IsSmartTerminal.
// The detection logic works as follows:
// 1. If the writer is of type *os.File, check if it's a terminal device
//   - First check if the TERM environment variable is set to "dumb"
//     If so, return false immediately
//   - Then use ioctl system call to get terminal attributes and
//     determine if it's actually a terminal
//
//  2. If the writer is of type fakeSmartTerminal (a mock terminal for testing),
//     return true
//  3. All other cases return false
func isSmartTerminal(w io.Writer) bool {
	// Attempt to convert the writer to *os.File to check if it's a file/terminal
	if f, ok := w.(*os.File); ok {
		// Check the TERM environment variable
		// If set to "dumb", it indicates a non-smart terminal that doesn't
		// support ANSI escape sequences or other advanced features
		if term, ok := os.LookupEnv("TERM"); ok && term == "dumb" {
			return false
		}
		// syscall.Termios is a structure used to store terminal attributes
		// We use ioctl system call with TCGETA command to get terminal attributes
		// and determine if this is actually a terminal device
		var termios syscall.Termios
		// syscall.Syscall6 is used to invoke the SYS_IOCTL system call
		// Parameter f.Fd() obtains the file descriptor
		// ioctlGetTermios is the ioctl command to get terminal attributes
		_, _, err := syscall.Syscall6(syscall.SYS_IOCTL, f.Fd(),
			ioctlGetTermios, uintptr(unsafe.Pointer(&termios)),
			0, 0, 0)
		// If ioctl call succeeds (err == 0), it's a terminal device
		return err == 0
	} else if _, ok := w.(*fakeSmartTerminal); ok {
		// fakeSmartTerminal is a mock smart terminal used for testing purposes
		return true
	}
	return false
}

// termSize retrieves the terminal's width and height (number of columns and rows).
// It uses the ioctl system call with TIOCGWINSZ command to get the terminal window size.
//
// Parameters:
//   - w: An io.Writer interface representing the terminal to query
//
// Return values:
//   - width: The number of columns (horizontal size)
//   - height: The number of rows (vertical size)
//   - ok: Returns true if size was successfully obtained, false otherwise
func termSize(w io.Writer) (width int, height int, ok bool) {
	// Check if the writer is of type *os.File
	if f, ok := w.(*os.File); ok {
		// winsize structure holds terminal window size information
		// wsRow: number of rows (height)
		// wsColumn: number of columns (width)
		// wsXpixel, wsYpixel: pixel dimensions (usually unused)
		var winsize struct {
			wsRow, wsColumn    uint16 // Terminal rows and columns
			wsXpixel, wsYpixel uint16 // Pixel dimensions in X and Y
		}
		// Use TIOCGWINSZ ioctl command to get window size
		// TIOCGWINSZ stands for "Terminal I/O Control Get WINdow SiZe"
		_, _, err := syscall.Syscall6(syscall.SYS_IOCTL, f.Fd(),
			syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&winsize)),
			0, 0, 0)
		// Convert uint16 to int and return success status
		return int(winsize.wsColumn), int(winsize.wsRow), err == 0
	} else if f, ok := w.(*fakeSmartTerminal); ok {
		// For mock terminals, return the preset width and height
		return f.termWidth, f.termHeight, true
	}
	// Non-terminal devices return zero values and failure status
	return 0, 0, false
}

// stripAnsiEscapes removes ANSI escape sequences from a byte slice in-place.
// ANSI escape sequences are used to control terminal cursor movement, colors,
// text styling, and other display attributes.
// This function modifies the input byte slice by removing all ANSI control codes.
//
// How it works:
// 1. Use two pointers (read and write) to process data in-place
// 2. Look for escape sequences starting with 0x1B (ESC character)
// 3. Identify CSI (Control Sequence Introducer) sequences, which start with ESC [
// 4. Find the sequence terminator (alphabetic characters a-z or A-Z) and skip the entire sequence
// 5. Copy non-escape characters forward to achieve in-place modification
//
// Parameter input: The byte slice to process
// Return value: The byte slice with ANSI escape sequences removed
func stripAnsiEscapes(input []byte) []byte {
	// read represents the remaining portion of input that needs processing
	read := input
	// write represents where we should write into input
	// It shares the same underlying array as input, enabling in-place modification
	write := input

	// advance is an inner function that copies 'count' bytes from read to write
	// and advances both slice positions
	advance := func(write, read []byte, count int) ([]byte, []byte) {
		copy(write, read[:count])
		return write[count:], read[count:]
	}

	for {
		// Search for the next escape sequence in the remaining read data
		// ANSI escape sequences start with the ESC character (0x1B)
		i := bytes.IndexByte(read, 0x1b)
		// If no escape sequence found, or not enough space for <ESC>[, we're done
		if i == -1 || i+1 >= len(read) {
			// Copy remaining non-escape characters to write position
			copy(write, read)
			break
		}

		// Check if this is a CSI (Control Sequence Introducer) sequence
		// CSI sequences format: ESC [ followed by parameters and terminator
		// If the next character is not '[', it's not a valid CSI sequence, continue searching
		if read[i+1] != '[' {
			write, read = advance(write, read, i+1)
			continue
		}

		// Found a CSI sequence, move write pointer to ESC character position
		// This will skip the ESC character but preserve content before it
		write, read = advance(write, read, i)

		// Find the CSI sequence terminator
		// CSI sequences end with an alphabetic character (a-z or A-Z)
		i = bytes.IndexFunc(read, func(r rune) bool {
			return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		})
		if i == -1 {
			// If no terminator found (incomplete sequence), remove all remaining content
			i = len(read) - 1
		}

		// Skip the terminator character
		i = i + 1

		// Advance the read pointer and reduce the final array length
		// This achieves in-place removal of ANSI sequences
		read = read[i:]
		input = input[:len(input)-i]
	}

	return input
}

// fakeSmartTerminal is a mock smart terminal structure used for testing purposes.
// It implements the io.Writer interface and stores preset terminal width and height.
// This allows testing features that require a smart terminal without relying on
// an actual terminal device.
//
// Embeds bytes.Buffer to implement the io.Writer interface
// termWidth and termHeight store the mock terminal's dimensions
type fakeSmartTerminal struct {
	bytes.Buffer              // Embedded Buffer to implement Write method
	termWidth, termHeight int // Mock terminal width and height (number of columns and rows)
}
