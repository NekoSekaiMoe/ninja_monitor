// Copyright 2018 Google Inc. All rights reserved.
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

// Package logger provides a simple logging interface for the ninja_monitor project.
// This package defines the Logger interface and SimpleLogger implementation, supporting multiple log levels:
// Fatal, Error, Verbose, Print, and Status.
// All log output is written to standard error (stderr) by default.
// The reason for using stderr instead of stdout is that logging output is considered diagnostic
// information rather than program output. This allows the user to separate program results (stdout)
// from diagnostic messages (stderr), enabling redirection of stdout to files while still seeing
// logs on the console, or piping only the actual output to other programs.
package logger

import (
	"fmt"
	"os"
)

// Logger is the interface for logging operations.
// This interface abstracts the logging behavior, allowing callers to use a unified interface
// for log output without knowing the implementation details.
// Implementations can provide different logging backends such as writing to files,
// sending to remote servers, or console output only.
type Logger interface {
	// Fatalf outputs a fatal error message and immediately terminates the program.
	// format is a format string, args are the formatting arguments.
	// This method outputs the error message and then exits the program with exit code 1.
	// It is typically used when encountering unrecoverable errors that prevent
	// the program from continuing safely.
	Fatalf(format string, args ...any)

	// Error outputs an error message.
	// msg is the error message content.
	// This method only outputs the error message without terminating the program.
	// It is used to log recoverable errors or issues that should be recorded but
	// do not prevent the program from continuing.
	Error(msg string)

	// Verbose outputs detailed log information.
	// msg is the log message content.
	// This method only outputs content when verbose mode is enabled.
	// It is used for debugging information or detailed execution progress,
	// helping developers understand the program's runtime state.
	Verbose(msg string)

	// Print outputs general log information.
	// msg is the log message content.
	// This method outputs the message without any prefix修饰 directly outputs the message content.
	// It is used for general log output such as operation results or progress indicators.
	Print(msg string)

	// Status outputs status information.
	// msg is the status message content.
	// This method is similar to Print, used for outputting program status
	// such as current progress or task status. It is typically used in
	// long-running tasks to report status to the user.
	Status(msg string)
}

// SimpleLogger is a basic implementation of the Logger interface that outputs to standard error (stderr).
// This struct provides a simple logger with the following capabilities:
// - Fatal: Fatal error output that terminates the program
// - Error: Error message output
// - Verbose: Detailed log output (optional, controlled by verbose field)
// - Print: General log output
// - Status: Status information output
//
// All output goes to stderr (standard error) because logging is diagnostic output,
// not program output. This allows users to separate actual results (stdout) from
// diagnostic messages (stderr), making it easier to filter or redirect logs independently.
//
// The verbose field controls whether Verbose-level logging is enabled:
// - When verbose is true, Verbose() will output log messages
// - When verbose is false, Verbose() produces no output
type SimpleLogger struct {
	verbose bool // Controls verbose log output mode: true enables Verbose logging, false disables it
}

// NewSimpleLogger creates and returns a new SimpleLogger instance.
// The verbose parameter controls verbose log output mode:
// - When verbose is true, the Verbose method will output log content
// - When verbose is false, the Verbose method will not output anything
// The returned SimpleLogger can be used directly for logging operations.
func NewSimpleLogger(verbose bool) *SimpleLogger {
	return &SimpleLogger{verbose: verbose}
}

// Fatalf outputs a fatal error message and immediately terminates the program.
// Uses fmt.Fprintf to write the formatted fatal error message to standard error (stderr).
// format is a format string that supports placeholder formatting,
// args are variadic parameters to fill the format placeholders.
// The output format is "FATAL: " prefix followed by the formatted message content, then a newline.
// After outputting, the function calls os.Exit(1) to terminate program execution with exit code 1.
func (l *SimpleLogger) Fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", args...)
	os.Exit(1)
}

// Error outputs an error message to standard error (stderr).
// Uses fmt.Fprintln to directly output the error message content.
// msg is the error message string.
// The output format is "ERROR: " prefix followed by the message content, then a newline.
// This method does not terminate program execution; it only logs the error information
// for the user or developer to review.
func (l *SimpleLogger) Error(msg string) {
	fmt.Fprintln(os.Stderr, "ERROR: "+msg)
}

// Errorf outputs a formatted error message to standard error (stderr).
// Uses fmt.Fprintf to write the formatted error message to standard error (stderr).
// format is a format string that supports placeholder formatting,
// args are variadic parameters to fill the format placeholders.
// The output format is "ERROR: " prefix followed by the formatted message content, then a newline.
// This method is similar to Error but supports formatted output, making it suitable
// for error messages that need to include variables.
func (l *SimpleLogger) Errorf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ERROR: "+format+"\n", args...)
}

// Verbose outputs detailed log information to standard error (stderr).
// Only outputs content when verbose mode is enabled (SimpleLogger.verbose is true).
// Uses fmt.Fprintln to directly output the message content.
// msg is the log message string.
// The output format is "VERBOSE: " prefix followed by the message content, then a newline.
// This method is used for debugging information or detailed execution progress,
// helping developers understand the program's runtime state, while not producing
// additional output in non-debug mode.
func (l *SimpleLogger) Verbose(msg string) {
	if l.verbose {
		fmt.Fprintln(os.Stderr, "VERBOSE: "+msg)
	}
}

// Print outputs general log information to standard error (stderr).
// Uses fmt.Fprintln to directly output the message content without any prefix.
// msg is the log message string, output content is exactly msg followed by a newline.
// This method is used for general log output such as operation results or progress indicators,
// with no specific level meaning.
func (l *SimpleLogger) Print(msg string) {
	fmt.Fprintln(os.Stderr, msg)
}

// Status outputs status information to standard error (stderr).
// Uses fmt.Fprintln to directly output the message content, similar to Print without any prefix.
// msg is the status message string, output content is exactly msg followed by a newline.
// This method is used for outputting program status information such as current progress or task status.
// It is typically used in long-running tasks to report execution status, helping users
// understand task progress.
func (l *SimpleLogger) Status(msg string) {
	fmt.Fprintln(os.Stderr, msg)
}
