// Copyright 2019 Google Inc. All rights reserved.
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

// Package terminal provides terminal output functionality for displaying build status
// and progress information in the terminal interface.
//
// This package contains two implementations:
// - simpleStatusOutput: Simple, straightforward output (this file)
// - smartStatusOutput: Rich terminal output with table mode and real-time updates (smart_status.go)
//
// When to use which:
// - Use simpleStatusOutput when:
//   - Outputting to a file or pipe (not an interactive terminal)
//   - Running in CI/CD environments where terminal features may not work
//   - You need minimal, portable output without ANSI codes
//   - The terminal does not support advanced features (colors, cursor control)
//
// - Use smartStatusOutput when:
//   - Outputting to an interactive terminal that supports ANSI codes
//   - You want to display a table of running actions in real-time
//   - You want color-coded output based on action duration
//   - You want a richer user experience with progress tracking
package terminal

import (
	"fmt"
	"io"

	"ninja_monitor/internal/status"
)

// simpleStatusOutput is a simple implementation of status.StatusOutput that mimics
// the built-in Ninja terminal output. It formats and writes build status information
// to a specified io.Writer.
//
// Key differences from smartStatusOutput (smart_status.go):
// - No table mode: Does not display a table of running actions
// - No real-time updates: Actions are displayed only when they complete
// - No threading: All output is synchronous, no goroutines
// - No signal handling: Does not handle terminal resize (SIGWINCH)
// - No mutex: Not thread-safe, but simpler code
// - Empty implementations: StartAction and Flush do nothing
//
// This simplicity makes simpleStatusOutput suitable for:
// - Non-interactive output (files, pipes, CI environments)
// - terminals that don't support ANSI escape sequences
// - Simple build systems that don't need advanced features
//
// Struct fields:
//   - writer: The io.Writer interface to write output to (typically os.Stdout or a file)
//   - formatter: The formatter used to convert status information to formatted strings
//     with colors and formatting
//   - keepANSI: Boolean flag controlling whether ANSI escape sequences (color codes, etc.)
//     are preserved in the output
//   - true: Keep ANSI sequences (colored output)
//   - false: Strip ANSI sequences (plain text output for terminals without color support)
//   - verbose: Boolean flag controlling whether verbose mode information is output
type simpleStatusOutput struct {
	writer    io.Writer
	formatter formatter
	keepANSI  bool
	verbose   bool
}

// NewSimpleStatusOutput creates a simple status output instance.
//
// This function returns a simpleStatusOutput that implements the status.StatusOutput
// interface. Unlike smartStatusOutput (which uses NewSmartStatusOutput or NewSmartStatusOutputWithHeight),
// this function takes a simpler set of parameters because simpleStatusOutput does not
// support table mode, threading, or signal handling.
//
// Parameters:
//   - w: io.Writer - The target writer for outputting status information (e.g., os.Stdout)
//   - formatter: formatter - The formatter to use for formatting status messages
//   - keepANSI: bool - Whether to preserve ANSI escape sequences in output
//     Set to false when outputting to terminals that don't support colors
//   - verbose: bool - Whether to output verbose mode information
//
// Returns:
//   - status.StatusOutput: An implementation of the status.StatusOutput interface
//     that provides simple Ninja-style build status output
//
// How it works:
//
// The returned simpleStatusOutput struct implements all methods required by the
// status.StatusOutput interface. It provides the following output behavior:
// - Message(): Prints messages to the writer, optionally stripping ANSI codes
// - StartAction(): Empty implementation (no action tracking for simple output)
// - FinishAction(): Prints action completion status with progress information
// - Flush(): Empty implementation (no buffer to flush)
// - Write(): Implements io.Writer interface for direct writing
//
// Compare with NewSmartStatusOutput (smart_status.go):
// - smartStatusOutput requires mutex protection for thread-safety
// - smartStatusOutput supports table mode for displaying multiple running actions
// - smartStatusOutput uses goroutines for real-time table updates
// - smartStatusOutput handles terminal resize signals (SIGWINCH)
func NewSimpleStatusOutput(w io.Writer, formatter formatter, keepANSI bool, verbose bool) status.StatusOutput {
	return &simpleStatusOutput{
		writer:    w,
		formatter: formatter,
		keepANSI:  keepANSI,
		verbose:   verbose,
	}
}

// Message processes and outputs a status message.
//
// This method handles messages of various importance levels, filtering based on the message
// level and outputting only important messages to the writer.
//
// Parameters:
//   - level: status.MsgLevel - The message level, determining the message's importance.
//     Messages below status.StatusLvl are typically ignored.
//   - message: string - The message content to output
//
// How it works:
//  1. First checks if the message level is >= status.StatusLvl
//     - Messages below this threshold are ignored (not output)
//     - This filters out verbose/debug messages in simple output mode
//  2. Uses the formatter to convert the message level and content into a formatted string
//     - The formatter adds colors, icons, and other formatting
//  3. If keepANSI is false, strips all ANSI escape sequences from the output
//     - This ensures compatibility with terminals that don't support colors
//     - Uses stripAnsiEscapes() to remove color codes and control sequences
//  4. Writes the formatted message to the writer using fmt.Fprintln
//     - Adds a newline after the message
//
// Compare with smartStatusOutput.Message() (smart_status.go:159):
// - smartStatusOutput distinguishes between different message levels more granularly
// - smartStatusOutput outputs verbose messages to stderr instead of ignoring them
// - smartStatusOutput can display messages in the status line or as regular output
// - smartStatusOutput uses mutex lock for thread-safety
//
// Notes:
// - Messages with level < status.StatusLvl are filtered out and not printed
// - This is the simple output behavior; smartStatusOutput treats this differently
func (s *simpleStatusOutput) Message(level status.MsgLevel, message string) {
	if level >= status.StatusLvl {
		output := s.formatter.message(level, message)
		if !s.keepANSI {
			output = string(stripAnsiEscapes([]byte(output)))
		}
		fmt.Fprintln(s.writer, output)
	}
}

// StartAction is called when an action starts executing.
//
// This is an empty implementation in simpleStatusOutput. Unlike smartStatusOutput,
// which tracks running actions in a table and updates the display in real-time,
// simpleStatusOutput does not track or display actions when they start.
//
// Parameters:
//   - action: *status.Action - The action that is starting, containing description and command
//   - counts: status.Counts - Current build statistics including pending, running, and completed counts
//
// How it works:
//
// This is intentionally a no-op (empty function body). In simpleStatusOutput:
// - Actions are not tracked in a table
// - No real-time display updates occur when actions start
// - The action's completion will be displayed in FinishAction instead
//
// Compare with smartStatusOutput.StartAction() (smart_status.go:183):
// - smartStatusOutput adds the action to a runningActions list
// - smartStatusOutput updates the status line to show current action
// - smartStatusOutput stores the start time for duration tracking
// - smartStatusOutput uses mutex for thread-safe access to running actions
//
// Why it's empty:
// - simpleStatusOutput focuses on simplicity and does not need to track running actions
// - The action's progress and result are shown when it completes in FinishAction
// - Real-time updates would require threading (goroutines), which simpleStatusOutput avoids
func (s *simpleStatusOutput) StartAction(action *status.Action, counts status.Counts) {
}

// FinishAction outputs result information when an action completes.
//
// This method displays the completion status of an action, including its progress
// (e.g., "[5/10]") and the action description or result.
//
// Parameters:
//   - result: status.ActionResult - The action result containing description, command,
//     error information, and other metadata
//   - counts: status.Counts - Current build statistics including completed/total counts
//
// How it works:
//  1. Gets the result description:
//     - Uses result.Description if available
//     - Falls back to result.Command if Description is empty
//  2. Generates progress information using the formatter:
//     - formatter.progress() creates a progress string like "[5/10] "
//     - This shows completed count vs total count
//  3. Generates result output using the formatter:
//     - formatter.result() creates a result string with status indicators
//     - Examples: "[OK]", "[FAIL]", "[WARN]"
//  4. If keepANSI is false, strips ANSI escape sequences from the result
//  5. Outputs the result:
//     - If result output is non-empty, outputs both progress and result on separate lines
//     - Otherwise, outputs only the progress line
//
// Output format examples:
//
//	Simple progress only:
//	  [5/10] Building main.go
//	With result indicator:
//	  [5/10] Building main.go
//	  [OK] Building completed
//
// Compare with smartStatusOutput.FinishAction() (smart_status.go:219):
// - smartStatusOutput removes the action from runningActions list
// - smartStatusOutput can suppress output after failures (postFailureActionCount)
// - smartStatusOutput uses mutex for thread-safety
// - smartStatusOutput can display command output if there are errors
func (s *simpleStatusOutput) FinishAction(result status.ActionResult, counts status.Counts) {
	str := result.Description
	if str == "" {
		str = result.Command
	}

	progress := s.formatter.progress(counts) + str

	output := s.formatter.result(result)
	if !s.keepANSI {
		output = string(stripAnsiEscapes([]byte(output)))
	}

	if output != "" {
		fmt.Fprint(s.writer, progress, "\n", output)
	} else {
		fmt.Fprintln(s.writer, progress)
	}
}

// Flush flushes the output buffer.
//
// This is an empty implementation in simpleStatusOutput. Unlike smartStatusOutput,
// which performs cleanup operations like stopping the table update ticker and restoring
// terminal state, simpleStatusOutput does not need any flush operations.
//
// How it works:
//
// This is intentionally a no-op (empty function body). In simpleStatusOutput:
// - No buffer is used that needs flushing
// - Output is written directly using fmt.Fprint/Fprintln
// - No background goroutines need to be stopped
// - No terminal state needs to be restored
//
// Compare with smartStatusOutput.Flush() (smart_status.go:266):
// - smartStatusOutput stops the action table update ticker
// - smartStatusOutput stops signal handling (SIGWINCH)
// - smartStatusOutput outputs post-failure completion messages
// - smartStatusOutput resets terminal state (scroll region, cursor visibility)
// - smartStatusOutput clears the running actions table
//
// Why it's empty:
// - simpleStatusOutput is designed for simplicity
// - No async operations that need cleanup
// - Direct write operations don't require buffering
func (s *simpleStatusOutput) Flush() {}

// Write implements the io.Writer interface's Write method.
//
// This method allows simpleStatusOutput to be used anywhere an io.Writer is expected.
// It writes the given byte slice to the underlying writer.
//
// Parameters:
//   - p: []byte - The byte slice to write
//
// Returns:
//   - int: The number of bytes written (always len(p))
//   - error: Error information (always nil in this implementation)
//
// How it works:
//  1. Converts the byte slice to a string using string(p)
//  2. Writes the string to the underlying writer using fmt.Fprint
//  3. Returns the length of the input as bytes written
//  4. Always returns nil for error (this implementation cannot produce errors)
//
// Notes:
// - This implementation always returns len(p) as bytes written
// - This implementation always returns nil for error
// - The conversion from []byte to string may involve copying, but the length is accurate
//
// Compare with smartStatusOutput.Write() (smart_status.go:314):
// - smartStatusOutput uses mutex lock for thread-safety
// - smartStatusOutput calls print() instead of fmt.Fprint directly
// - Both implementations return the same values
func (s *simpleStatusOutput) Write(p []byte) (int, error) {
	fmt.Fprint(s.writer, string(p))
	return len(p), nil
}
