// Copyright 2018 Google Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package terminal provides a set of interfaces for interacting with terminals.
// These interfaces allow graceful degradation when terminal I/O is redirected
// or when running in non-interactive terminal environments.
// This package abstracts standard input/output/error streams, allowing upper-layer
// logic to remain agnostic about whether data comes from a real terminal or from
// redirected pipes/files.
package terminal

import (
	"io"
	"os"
)

// StdioInterface is an abstraction for standard input/output/error streams.
// It defines methods to access stdin, stdout, and stderr streams.
// This interface decouples the program from concrete stream implementations,
// enabling support for:
//   - Using OS-native standard streams (default behavior)
//   - Using custom Reader/Writer implementations (for testing or redirection)
//   - Flexibly switching terminal interaction methods in different environments
type StdioInterface interface {
	// Stdin returns the standard input reader.
	// In interactive terminals, this typically receives keyboard input;
	// In redirected scenarios, this may read from pipes or files.
	Stdin() io.Reader

	// Stdout returns the standard output writer.
	// In interactive terminals, this typically displays to the screen;
	// In redirected scenarios, this may write to pipes or files.
	Stdout() io.Writer

	// Stderr returns the standard error output writer.
	// Used for error messages, typically separate from stdout,
	// allowing users to handle normal output and error output separately.
	Stderr() io.Writer
}

// StdioImpl is the default implementation of StdioInterface using OS-provided standard streams.
// When the program runs in a real terminal environment, this implementation directly accesses
// os.Stdin, os.Stdout, and os.Stderr.
// This allows the program to receive data from user keyboard input
// and display output to the terminal screen.
type StdioImpl struct{}

// Stdin returns the operating system standard input stream (os.Stdin).
// This is the program's default input source, typically connected to terminal keyboard input or pipe output.
// The returned io.Reader can be used to read user input or pipe data.
func (StdioImpl) Stdin() io.Reader { return os.Stdin }

// Stdout returns the operating system standard output stream (os.Stdout).
// This is the program's default output destination, typically connected to terminal screen or pipe input.
// The returned io.Writer can be used to output normal program results.
func (StdioImpl) Stdout() io.Writer { return os.Stdout }

// Stderr returns the operating system standard error output stream (os.Stderr).
// This is the program's default error output destination, typically connected to terminal screen
// (but may be separate from stdout).
// The returned io.Writer can be used to output error messages or debug information.
func (StdioImpl) Stderr() io.Writer { return os.Stderr }

// var _ StdioInterface = StdioImpl{} is a compile-time assertion
// that ensures the StdioImpl struct implements all methods required by StdioInterface.
// If StdioImpl is missing any methods required by the interface, compilation will fail.
// This pattern helps catch incomplete interface implementations at compile time.
var _ StdioInterface = StdioImpl{}

// customStdio is an alternative implementation of StdioInterface that allows custom standard streams.
// This is particularly useful in the following scenarios:
//   - Unit testing: inject mock Reader/Writer to simulate input/output
//   - Redirection: redirect program standard streams to files or memory buffers
//   - Pipe communication: build advanced features that need to read/write pipes
type customStdio struct {
	stdin  io.Reader // custom standard input stream
	stdout io.Writer // custom standard output stream
	stderr io.Writer // custom standard error stream
}

// NewCustomStdio creates a custom StdioImpl instance.
// By passing custom stdin, stdout, and stderr,
// this function allows replacing the program's standard streams.
//
// Parameters:
//   - stdin: Custom standard input Reader, can be any object implementing io.Reader interface
//   - stdout: Custom standard output Writer, can be any object implementing io.Writer interface
//   - stderr: Custom standard error Writer, can be any object implementing io.Writer interface
//
// Return value:
//   - Returns an object implementing StdioInterface, can be used to replace default standard streams
//
// Usage examples:
//   - Redirect output to file: NewCustomStdio(os.Stdin, file, file)
//   - Memory buffer testing: NewCustomStdio(bytes.NewBufferString("input"), &buf, &buf)
func NewCustomStdio(stdin io.Reader, stdout, stderr io.Writer) StdioInterface {
	return customStdio{stdin, stdout, stderr}
}

// Stdin returns the custom standard input stream.
// Directly returns the stored stdin Reader from the struct without any processing.
func (c customStdio) Stdin() io.Reader { return c.stdin }

// Stdout returns the custom standard output stream.
// Directly returns the stored stdout Writer from the struct without any processing.
func (c customStdio) Stdout() io.Writer { return c.stdout }

// Stderr returns the custom standard error stream.
// Directly returns the stored stderr Writer from the struct without any processing.
func (c customStdio) Stderr() io.Writer { return c.stderr }

// var _ StdioInterface = customStdio{} is a compile-time assertion
// that ensures the customStdio struct implements all methods required by StdioInterface.
// If customStdio is missing any methods required by the interface, compilation will fail.
var _ StdioInterface = customStdio{}
