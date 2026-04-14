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

package terminal

import (
	"fmt"
	"strings"
	"time"

	"ninja_monitor/internal/status"
)

// formatter is a struct used for formatting terminal output.
// It formats status and progress information in a format similar to the Ninja build system.
// This formatter supports custom format strings similar to NINJA_STATUS environment variable,
// allowing users to customize how build progress is displayed in the terminal.
//
// Fields:
//   - colorize: enables/disables colored output using ANSI escape sequences
//   - format: custom format string (similar to NINJA_STATUS environment variable)
//   - quiet: quiet mode - suppresses detailed output
//   - verbose: verbose mode - outputs additional debug information
//   - start: timestamp when the formatter was created, used for calculating elapsed time
type formatter struct {
	colorize bool      // whether to enable colored output (using ANSI escape sequences)
	format   string    // custom format string, similar to NINJA_STATUS environment variable
	quiet    bool      // quiet mode - do not output detailed information
	verbose  bool      // verbose mode - output more debug information
	start    time.Time // timestamp when the formatter was created, used for calculating elapsed time
}

// NewFormatter creates a new formatter for formatting terminal output.
// The arguments are almost identical to the options supported by the NINJA_STATUS environment variable
// in the Ninja build system. The %c placeholder is currently not supported.
//
// Parameters:
//   - colorize: whether to enable colored output
//   - format: custom format string; if empty, the default format will be used
//   - quiet: quiet mode - suppress detailed output
//   - verbose: verbose mode - output more debug information
//
// Returns:
//
//	A formatter instance configured with the given parameters
func NewFormatter(colorize bool, format string, quiet bool, verbose bool) formatter {
	return newFormatter(colorize, format, quiet, verbose)
}

// newFormatter is the internal implementation of NewFormatter.
// Creates a formatter instance and records the start time for elapsed time calculations.
//
// Parameters:
//   - colorize: whether to enable colored output
//   - format: custom format string; if empty, the default format will be used
//   - quiet: quiet mode - suppress detailed output
//   - verbose: verbose mode - output more debug information
//
// Returns:
//
//	A formatter instance with all fields initialized
func newFormatter(colorize bool, format string, quiet bool, verbose bool) formatter {
	return formatter{
		colorize: colorize,
		format:   format,
		quiet:    quiet,
		verbose:  verbose,
		start:    time.Now(),
	}
}

// message formats a message string according to its message level.
// This method handles different message levels (error, warning, status, etc.)
// and adds appropriate prefixes and colors.
//
// Parameters:
//   - level: the message level (e.g., ErrorLvl, WarningLvl, StatusLvl from status package)
//   - message: the message content to be formatted
//
// Returns:
//
//	The formatted string with appropriate prefix and color, or empty string if message level is below status level
//
// Behavior:
//   - For ErrorLvl and above: displays as failed status with "FAILED:" prefix
//   - For levels between StatusLvl and ErrorLvl (e.g., Warning): adds prefix from level.Prefix()
//   - For StatusLvl: special handling for Ninja messages; messages starting with "ninja:" are displayed in bright white
func (s formatter) message(level status.MsgLevel, message string) string {
	// Error level and above messages are displayed as failed status
	if level >= status.ErrorLvl {
		return fmt.Sprintf("%s %s", s.failedString(), message)
	} else if level > status.StatusLvl {
		// Intermediate levels like warnings are prefixed
		return fmt.Sprintf("%s%s", level.Prefix(), message)
	} else if level == status.StatusLvl {
		// Special handling for Ninja messages: messages starting with "ninja:" are displayed in bright white
		if s.colorize && len(message) > 6 && message[:6] == "ninja:" {
			return ansi.brightWhite() + message + ansi.regular()
		}
		return message
	}
	return ""
}

// remainingTimeString calculates and returns the string representation of remaining time.
// If the estimated completion time is in the future, returns the remaining time rounded to seconds.
//
// Parameters:
//   - t: the estimated completion time
//
// Returns:
//
//	String representation of remaining time (e.g., "1h2m3s"), or empty string if time has passed
func remainingTimeString(t time.Time) string {
	now := time.Now()
	if t.After(now) {
		return t.Sub(now).Round(time.Duration(time.Second)).String()
	}
	return ""
}

// progress formats and returns the string representation of the current build progress.
// Uses either the default format or a custom format string depending on configuration.
//
// Default format displays: completion percentage, finished/total task count, estimated remaining time,
// and number of currently running tasks.
//
// Parameters:
//   - counts: a status.Counts struct containing various count information
//
// Returns:
//
//	Formatted progress string for display in the terminal
//
// Custom format placeholders supported (similar to NINJA_STATUS):
//
//	%s - Number of started tasks (actions that have been initiated)
//	%t - Total number of tasks to be executed
//	%r - Number of currently running tasks
//	%u - Number of unstarted tasks (pending task count = TotalActions - StartedActions)
//	%f - Number of finished/completed tasks
//	%o - Output rate: tasks completed per second (FinishedActions / elapsed seconds)
//	%c - Currently unimplemented placeholder (displays as '?')
//	%p - Completion percentage (100 * FinishedActions / TotalActions)
//	%e - Elapsed time in seconds (with 3 decimal places precision)
//	%l - Estimated remaining time (e.g., "1h2m3s", or '?' if no estimate available)
//
// Example default output: "[75% 150/200 5m30s remaining]"
// Example verbose output with 2 running jobs: "[75% 150/200 5m30s remaining] (2 jobs)"
func (s formatter) progress(counts status.Counts) string {
	// If no custom format is set, use the default format
	if s.format == "" {
		output := ""
		// If colorize is enabled, use bold blue color
		if s.colorize {
			output += ansi.boldBlue()
		}
		// Default format: [percentage finished/total]
		// %3d pads the percentage to 3 characters (e.g., " 75" instead of "75")
		// %% outputs a literal percent sign
		output += fmt.Sprintf("[%3d%% %d/%d", 100*counts.FinishedActions/counts.TotalActions, counts.FinishedActions, counts.TotalActions)

		// If there's an estimated remaining time, display it
		// The "remaining" label is appended after the time string
		if remaining := remainingTimeString(counts.EstimatedTime); remaining != "" {
			output += fmt.Sprintf(" %s remaining", remaining)
		}
		output += "]"

		// If verbose mode is enabled and there are running tasks, display running job count
		// Uses bold green color for this section
		if s.verbose && counts.RunningActions > 0 {
			if s.colorize {
				output += ansi.regular()
				output += ansi.boldGreen()
			}
			// Handle singular/plural: "job" vs "jobs"
			if counts.RunningActions == 1 {
				output += fmt.Sprintf("(%d job)", counts.RunningActions)
			} else {
				output += fmt.Sprintf("(%d jobs)", counts.RunningActions)
			}
		}
		// Reset to regular color/weight before adding trailing space
		if s.colorize {
			output += ansi.regular()
		}
		output += " "

		return output
	}

	// Custom format string parsing and formatting
	// Supports all the placeholders listed above
	// This parsing is done character by character to handle the % prefix
	//
	// Format string processing:
	//   - Characters other than '%' are written directly to output
	//   - "%%" outputs a single '%'
	//   - "%X" where X is a placeholder letter is replaced with the corresponding value
	//   - Unknown placeholders are output as "unknown placeholder 'X'"
	//
	// Implementation details:
	//   - Uses strings.Builder for efficient string concatenation
	//   - Iterates through format string, checking each character
	//   - When '%' is encountered, looks at the next character to determine the placeholder
	//   - Each case writes the appropriate formatted value to the builder
	buf := &strings.Builder{}
	for i := 0; i < len(s.format); i++ {
		c := s.format[i]
		if c != '%' {
			buf.WriteByte(c)
			continue
		}

		i = i + 1
		if i == len(s.format) {
			// Handle trailing '%' at end of format string - write it as-is
			buf.WriteByte(c)
			break
		}

		c = s.format[i]
		switch c {
		case '%':
			// %% outputs a literal percent sign
			buf.WriteByte(c)
		case 's':
			// %s: Number of started actions/tasks
			// %d formats as decimal integer
			fmt.Fprintf(buf, "%d", counts.StartedActions)
		case 't':
			// %t: Total number of actions/tasks
			fmt.Fprintf(buf, "%d", counts.TotalActions)
		case 'r':
			// %r: Number of currently running actions/tasks
			fmt.Fprintf(buf, "%d", counts.RunningActions)
		case 'u':
			// %u: Number of unstarted (pending) actions/tasks
			// Calculated as TotalActions - StartedActions
			fmt.Fprintf(buf, "%d", counts.TotalActions-counts.StartedActions)
		case 'f':
			// %f: Number of finished/completed actions/tasks
			fmt.Fprintf(buf, "%d", counts.FinishedActions)
		case 'o':
			// %o: Output rate - tasks completed per second
			// %.1f formats as floating point with 1 decimal place
			// Calculation: FinishedActions / seconds since start
			// time.Since(s.start) returns duration since formatter creation
			// .Seconds() converts duration to seconds as float64
			fmt.Fprintf(buf, "%.1f", float64(counts.FinishedActions)/time.Since(s.start).Seconds())
		case 'c':
			// %c: Currently not implemented (TODO: implement?)
			// Outputs '?' as placeholder
			buf.WriteRune('?')
		case 'p':
			// %p: Completion percentage
			// %3d%% formats as 3-digit padded decimal + literal percent sign
			// Example: 75 becomes " 75%"
			fmt.Fprintf(buf, "%3d%%", 100*counts.FinishedActions/counts.TotalActions)
		case 'e':
			// %e: Elapsed time in seconds
			// %.3f formats with 3 decimal places (millisecond precision)
			// time.Since(s.start) calculates duration since formatter was created
			fmt.Fprintf(buf, "%.3f", time.Since(s.start).Seconds())
		case 'l':
			// %l: Estimated remaining time
			// If EstimatedTime is zero (not set), display '?'
			// Otherwise, display the remaining time string
			if counts.EstimatedTime.IsZero() {
				buf.WriteRune('?')
			} else {
				// remainingTimeString handles the calculation and formatting
				fmt.Fprintf(buf, "%s", remainingTimeString(counts.EstimatedTime))
			}
		default:
			// Unknown placeholder - output error message with the unknown character
			buf.WriteString("unknown placeholder '")
			buf.WriteByte(c)
			buf.WriteString("'")
		}
	}
	return buf.String()
}

// result formats and returns the string representation of an action result.
// Handles both failure and success cases with appropriate formatting.
//
// Parameters:
//   - result: a status.ActionResult struct containing the action result,
//     including error information, output files, command, and output
//
// Returns:
//
//	Formatted result string ready for terminal output
//
// Behavior:
//   - If there is an error (result.Error != nil):
//   - Formats failure output with "FAILED:" prefix
//   - If quiet mode is off and command is available, includes command in output
//   - Shows target/output files and any error output
//   - If operation succeeded but there is output (result.Output != ""):
//   - Returns the output directly
//   - Ensures the returned string always ends with a newline character
func (s formatter) result(result status.ActionResult) string {
	var ret string
	// If there is error information, format the failure output
	if result.Error != nil {
		// Join multiple output files with space separator
		targets := strings.Join(result.Outputs, " ")
		if s.quiet || result.Command == "" {
			// In quiet mode or when command is not to be displayed,
			// only show failure message and outputs
			ret = fmt.Sprintf("%s %s\n%s", s.failedString(), targets, result.Output)
		} else {
			// Show failure message, outputs, command, and error output
			ret = fmt.Sprintf("%s %s\n%s\n%s", s.failedString(), targets, result.Command, result.Output)
		}
	} else if result.Output != "" {
		// Operation succeeded and there is output - return output directly
		ret = result.Output
	}

	// Ensure the return value ends with a newline character
	// This is important for proper terminal output formatting
	if len(ret) > 0 && ret[len(ret)-1] != '\n' {
		ret += "\n"
	}

	return ret
}

// failedString returns the failure message prefix string.
// If colored output is enabled, uses red bold ANSI sequences for visual emphasis.
//
// Returns:
//
//	"FAILED:" or "FAILED:" with red bold formatting
//
// The ANSI sequences used:
//   - ansi.red() - sets text color to red
//   - ansi.bold() - enables bold text weight
//   - ansi.regular() - resets to default color and weight
func (s formatter) failedString() string {
	failed := "FAILED:"
	if s.colorize {
		// Use red bold color for failure messages
		// Pattern: [color][weight]text[reset]
		failed = ansi.red() + ansi.bold() + failed + ansi.regular()
	}
	return failed
}
