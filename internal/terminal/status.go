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

// Package terminal provides terminal output functionality for simulating
// Ninja build system's built-in terminal output effects. This package is
// responsible for generating formatted build status output based on different
// terminal types and configurations.
package terminal

import (
	"io"

	"ninja_monitor/internal/status"
)

// NewStatusOutput creates and returns a StatusOutput interface implementation
// for displaying the current build status. This function is designed to
// simulate the built-in terminal output of the Ninja build tool, providing
// rich progress display functionality.
//
// Parameters:
//   - w io.Writer: The output destination writer, typically stdout or a file
//   - statusFormat string: Status format string, similar to NINJA_STATUS environment
//     variable, supports the following placeholders:
//     %f - Number of targets currently being executed
//     %s - Number of completed targets
//     %t - Total number of targets
//     %p - Progress percentage
//     %r - Name of the target currently being executed
//     %u - Number of targets being executed (completed)
//     %j - Number of completed targets
//     Note: %c is currently not supported
//   - forceSimpleOutput bool: Force simple output mode, ignoring terminal detection
//   - quietBuild bool: Quiet build mode, do not show detailed progress information
//   - forceKeepANSI bool: Force preservation of ANSI escape sequences for colored output
//   - verbose bool: Verbose mode, output more debug information
//
// Return value:
//   - status.StatusOutput: Returns a concrete implementation of the status output
//     interface. Returns smart output or simple output depending on terminal type.
//
// How it works:
//  1. First detects if the terminal supports smart formatting (isSmartTerminal)
//  2. If forceSimpleOutput is true, forces simple output mode
//  3. Creates the appropriate formatter based on detection result
//  4. If terminal supports smart formatting, returns NewSmartStatusOutputWithHeight
//  5. Otherwise returns NewSimpleStatusOutput
//
// Example usage:
//
//	output := NewStatusOutput(os.Stdout, "[%f/%t] ", false, false, false, false)
func NewStatusOutput(w io.Writer, statusFormat string, forceSimpleOutput, quietBuild, forceKeepANSI bool, verbose bool) status.StatusOutput {
	// Determine if smart formatting can be used.
	// Returns true only if forceSimpleOutput is false AND the terminal is a smart terminal.
	canUseSmartFormatting := !forceSimpleOutput && isSmartTerminal(w)

	// Create a formatter to convert build status into formatted strings.
	// The formatter decides whether to use advanced features like colors,
	// progress bars, etc. based on terminal type and configuration.
	formatter := newFormatter(canUseSmartFormatting, statusFormat, quietBuild, verbose)

	// Choose the appropriate output implementation based on whether smart formatting is supported.
	if canUseSmartFormatting {
		// Smart terminal output: supports progress bars, colored output, dynamic updates, etc.
		return NewSmartStatusOutputWithHeight(w, formatter, 0, verbose)
	} else {
		// Simple terminal output: suitable for terminals that don't support advanced features,
		// such as file redirection or non-interactive output.
		return NewSimpleStatusOutput(w, formatter, forceKeepANSI, verbose)
	}
}
