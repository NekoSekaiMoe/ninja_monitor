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

// Package terminal provides terminal output functionality for displaying build status,
// progress information, and running actions in a smart, formatted manner similar to
// Ninja's built-in terminal output.
//
// This package implements the status.StatusOutput interface and provides:
// - Real-time display of running build actions in a table format
// - Progress tracking with counts (total, completed, failed, etc.)
// - ANSI escape sequence support for colors, cursor control, and scrolling regions
// - Terminal resize handling via SIGWINCH signals
// - Thread-safe operation using mutex synchronization
package terminal

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"ninja_monitor/internal/status"
)

// tableHeightEnVar is the environment variable name used to customize the action table height.
// Users can set this environment variable to control how many rows are displayed in the
// action table. For example, setting SOONG_UI_TABLE_HEIGHT=5 will display 5 rows of running actions.
//
// This allows users to override the default behavior which automatically calculates
// the table height based on terminal height (typically 1/4 of terminal height, clamped between 1-10).
const tableHeightEnVar = "SOONG_UI_TABLE_HEIGHT"

// actionTableEntry represents an entry in the running action table.
// Each entry contains information about an action that is currently being executed
// and the time it started.
//
// This struct is used to track all running actions and display them in the
// action table at the bottom of the terminal. The startTime is used to calculate
// the duration each action has been running, which determines the color coding
// (normal, yellow for 30+ seconds, red for 60+ seconds).
//
// Fields:
//   - action: A pointer to the status.Action struct containing detailed information
//     about the action including its description, command, and metadata.
//   - startTime: The time.Time when the action started, used to calculate elapsed
//     duration for display in the table.
type actionTableEntry struct {
	action    *status.Action // The action being executed with its details
	startTime time.Time      // When the action started, for calculating duration
}

// smartStatusOutput is the main struct that implements a smart terminal status output,
// similar to Ninja's built-in terminal output. It handles formatting and displaying
// various build status information in real-time.
//
// This struct provides the following functionality:
// - Displays running actions in a table at the bottom of the terminal
// - Shows progress information (total, done, failed, etc.)
// - Supports table mode for displaying multiple running actions simultaneously
// - Handles terminal resize via SIGWINCH signals
// - Uses ANSI escape sequences for colors, cursor control, and scrolling regions
// - Provides thread-safe operation with mutex synchronization
//
// The smart status output divides the terminal into two regions:
// 1. Scrolling region: The upper area where command output and messages are displayed
// 2. Non-scrolling region: The bottom rows reserved for the action table
//
// Thread Safety:
// All methods that access shared state (runningActions, tableMode, term dimensions, etc.)
// use the lock mutex to ensure thread-safe access. This is particularly important because
// there are background goroutines (ticker for updating the table and signal handler for
// resize events) that also access the shared state.
type smartStatusOutput struct {
	writer    io.Writer // The output writer, typically os.Stdout
	formatter formatter // The formatter used to format messages, progress, and results

	lock sync.Mutex // Mutex protecting all shared state for thread-safe access

	haveBlankLine bool // Flag indicating whether a blank line has been output;
	// used to avoid duplicate newlines between outputs

	// Table mode configuration
	tableMode             bool // Whether table mode is enabled; when true, displays running actions in a table
	tableHeight           int  // The actual computed height of the action table in rows
	requestedTableHeight  int  // The user-requested table height; 0 means auto-calculate, negative means disabled
	termWidth, termHeight int  // Current terminal dimensions in characters (width x height)

	// Running actions tracking
	runningActions  []actionTableEntry // List of currently running actions, ordered by start time
	ticker          *time.Ticker       // Time ticker that fires every second to update the action table display
	done            chan bool          // Signal channel to stop the ticker goroutine
	sigwinch        chan os.Signal     // Channel for receiving SIGWINCH signals (terminal resize events)
	sigwinchHandled chan bool          // Optional callback channel to signal that SIGWINCH has been handled

	verbose bool // Whether verbose output mode is enabled; when true, shows full command instead of shortened description

	// Failure handling
	// haveFailures tracks whether any action has failed during the build.
	// Once a failure occurs, subsequent command outputs are suppressed to make
	// it easier for users to find error messages in the output.
	haveFailures bool
	// postFailureActionCount counts how many actions completed after a failure occurred.
	// When outputs are being discarded due to earlier failures, this count is used
	// to display a message at the end telling the user to check verbose.log.gz for those outputs.
	postFailureActionCount int
}

// NewSmartStatusOutput creates and returns a new StatusOutput implementation.
// This implementation mimics Ninja's built-in terminal output, providing real-time
// display of build status with progress information and running actions.
//
// Parameters:
//   - w: The output writer where status information will be written (typically os.Stdout)
//   - formatter: The formatter used to format various output content including messages,
//     progress strings, and action results
//
// Returns:
//
//	A smartStatusOutput implementing the status.StatusOutput interface,
//	with default table height (auto-calculated based on terminal height)
func NewSmartStatusOutput(w io.Writer, formatter formatter) status.StatusOutput {
	return NewSmartStatusOutputWithHeight(w, formatter, 0, false)
}

// NewSmartStatusOutputWithHeight creates a smart status output with a specified table height.
// This function is the main constructor that initializes all components of the smart status output.
//
// Parameters:
//   - w: The output writer where status information will be written
//   - formatter: The formatter used to format messages, progress, and results
//   - tableHeight: The requested table height; 0 means auto-calculate based on terminal height,
//     positive values use the specified height, negative values disable table mode
//   - verbose: Whether to enable verbose output mode (shows full commands instead of shortened descriptions)
//
// Returns:
//
//	A smartStatusOutput implementing the status.StatusOutput interface
//
// Initialization process:
//  1. Create the smartStatusOutput struct with default values
//  2. Check for tableHeight parameter or SOONG_UI_TABLE_HEIGHT environment variable
//  3. Get terminal size and compute appropriate table height if table mode is enabled
//  4. If table mode is enabled:
//     - Print blank lines at the bottom to reserve space for the action table
//     - Hide the cursor to avoid visual flicker during updates
//     - Initialize the action table display
//     - Start the ticker goroutine that updates the table every second
//  5. Start the SIGWINCH signal handler to handle terminal resize events
func NewSmartStatusOutputWithHeight(w io.Writer, formatter formatter, tableHeight int, verbose bool) status.StatusOutput {
	s := &smartStatusOutput{
		writer:    w,
		formatter: formatter,

		haveBlankLine: true,

		tableMode: true, // Default to table mode enabled
		verbose:   verbose,

		done:     make(chan bool),
		sigwinch: make(chan os.Signal),
	}

	// Use provided tableHeight or fall back to environment variable
	if tableHeight != 0 {
		s.tableMode = tableHeight > 0
		s.requestedTableHeight = tableHeight
	} else if env, ok := os.LookupEnv(tableHeightEnVar); ok {
		h, _ := strconv.Atoi(env)
		s.tableMode = h > 0
		s.requestedTableHeight = h
	}

	// Get terminal size; if successful compute table height, otherwise disable table mode
	if w, h, ok := termSize(s.writer); ok {
		s.termWidth, s.termHeight = w, h
		s.computeTableHeight()
	} else {
		s.tableMode = false
	}

	if s.tableMode {
		// Add blank lines at the bottom of the screen to allow scrolling to see history
		// and to make room for the action table
		// TODO: Read cursor position to determine if these blank lines are needed
		for i := 0; i < s.tableHeight; i++ {
			fmt.Fprintln(w)
		}

		// Hide the cursor to avoid seeing it jump around during updates
		fmt.Fprintf(s.writer, ansi.hideCursor())

		// Initialize the empty action table (first display)
		s.actionTable()

		// Start the ticker to periodically update the action table
		s.startActionTableTick()
	}

	// Start SIGWINCH signal handler to handle terminal resize events
	s.startSigwinch()

	return s
}

// Message outputs a message at the specified level.
// This method implements the StatusOutput interface and handles different message
// levels appropriately.
//
// Parameters:
//   - level: The message level (MsgLevel) that determines how the message is displayed.
//     Levels below StatusLvl are verbose messages, equal to StatusLvl are status line
//     messages, and above StatusLvl are printed directly.
//   - message: The message string to be output
//
// How it works:
//   - If the message level is below StatusLvl (verbose level), the message is printed
//     to stderr instead of the terminal status area, as these are typically diagnostic
//     messages that don't need to clutter the status display
//   - If the message level is above StatusLvl, the message is printed directly to the
//     output writer
//   - If the message level equals StatusLvl, the message is displayed as a status line
//     at the bottom of the terminal (in the non-scrolling region when table mode is active)
//
// Thread Safety:
//
//	This method acquires the lock mutex to ensure thread-safe access to shared state
//	(haveBlankLine, tableMode, etc.)
func (s *smartStatusOutput) Message(level status.MsgLevel, message string) {
	if level < status.StatusLvl {
		// Verbose messages go to stderr, not to the terminal status area
		fmt.Fprintln(os.Stderr, message)
		return
	}

	str := s.formatter.message(level, message)

	s.lock.Lock()
	defer s.lock.Unlock()

	if level > status.StatusLvl {
		s.print(str)
	} else {
		s.statusLine(str)
	}
}

// StartAction begins displaying the execution status of an action.
// This method is called when an action starts executing. It adds the action to the
// running actions list and displays its description in the status line.
//
// Parameters:
//   - action: A pointer to the status.Action struct containing the action's details
//     including its description, command, and any other metadata
//   - counts: The current build statistics (total count, done count, failed count, etc.)
//     that are used to display progress information
//
// How it works:
//  1. Records the current time as the action's start time
//  2. Extracts the action description (preferring Description field over Command field)
//  3. Gets the formatted progress string from the formatter
//  4. Acquires the lock to ensure thread-safe access to runningActions
//  5. Adds the action to the runningActions list with its start time
//  6. Displays the progress and action description in the status line
//
// The action remains in the runningActions list until FinishAction is called,
// allowing it to be displayed in the action table during the periodic updates.
func (s *smartStatusOutput) StartAction(action *status.Action, counts status.Counts) {
	startTime := time.Now()

	// Get action description, preferring Description field over Command
	str := action.Description
	if str == "" {
		str = action.Command
	}

	// Get the formatted progress information
	progress := s.formatter.progress(counts)

	s.lock.Lock()
	defer s.lock.Unlock()

	// Add the action to the running actions list
	s.runningActions = append(s.runningActions, actionTableEntry{
		action:    action,
		startTime: startTime,
	})

	// Display progress and action description in the status line
	s.statusLine(progress + str)
}

// FinishAction completes the display of an action.
// This method is called when an action finishes executing. It removes the action
// from the running actions list and displays the result.
//
// Parameters:
//   - result: The status.ActionResult containing the action's result information,
//     including description, command, error status, and any error output
//   - counts: The current build statistics used for displaying progress
//
// How it works:
//  1. Extracts the result description (preferring Description over Command)
//  2. Formats the progress string and result string using the formatter
//  3. Acquires the lock to access runningActions
//  4. Removes the completed action from runningActions list
//  5. Displays the progress information in the status line
//  6. If there was an error, sets haveFailures flag
//  7. If haveFailures is already true and current action has no error,
//     delays output to avoid cluttering error messages
//  8. Increments postFailureActionCount if output is being delayed
//
// Failure handling logic:
//   - Once any action fails (haveFailures becomes true), subsequent command outputs
//     are suppressed to make it easier for users to find error messages
//   - However, actions that have their own errors (独立错误输出) are still displayed
//   - At the end (in Flush), a message is printed telling users to check verbose.log.gz
//     for the delayed outputs
func (s *smartStatusOutput) FinishAction(result status.ActionResult, counts status.Counts) {
	// Get result description, preferring Description field over Command
	str := result.Description
	if str == "" {
		str = result.Command
	}

	// Format progress and result information
	progress := s.formatter.progress(counts) + str
	output := s.formatter.result(result)

	s.lock.Lock()
	defer s.lock.Unlock()

	// Remove the completed action from the running list
	for i, runningAction := range s.runningActions {
		if runningAction.action == result.Action {
			s.runningActions = append(s.runningActions[:i], s.runningActions[i+1:]...)
			break
		}
	}

	// Display progress information
	s.statusLine(progress)

	// When failures exist, stop printing command outputs to make errors easier to find,
	// but don't skip actions that have their own errors (独立错误输出)
	if output != "" {
		if !s.haveFailures || result.Error != nil {
			s.requestLine()
			s.print(output)
		} else {
			s.postFailureActionCount++
		}
	}

	// Record failure status
	if result.Error != nil {
		s.haveFailures = true
	}
}

// Flush flushes output and cleans up state.
// This method is called when the build completes or ends. It performs necessary cleanup:
//
//   - Stops the action table update ticker (if table mode is enabled)
//   - Outputs a message about actions that completed after a failure (if any)
//   - Resets terminal state (cursor visibility, scrolling region, etc.)
//   - Clears the running actions list
//
// How it works:
//  1. First stops the ticker OUTSIDE the lock to avoid deadlock - the ticker's
//     goroutine might be blocked on s.lock when trying to receive from s.done
//  2. Acquires the lock for remaining cleanup operations
//  3. Stops the SIGWINCH signal handler
//  4. If there were actions that completed after a failure, prints a message
//     telling the user to check verbose.log.gz for their output
//  5. Clears the running actions list
//  6. If table mode was enabled:
//     - Updates the action table to clear the display (show empty table)
//     - Resets scrolling margins to cover the entire terminal
//     - Moves cursor to the line above where the table was (now blank)
//     - Shows the cursor again
func (s *smartStatusOutput) Flush() {
	if s.tableMode {
		// Stop the table ticker OUTSIDE the lock to avoid deadlock
		// The goroutine in startActionTableTick might be blocked on s.lock
		// and unable to read from s.done channel
		s.stopActionTableTick()
	}

	s.lock.Lock()
	defer s.lock.Unlock()

	// Stop SIGWINCH signal handling
	s.stopSigwinch()

	// If there were actions that completed after a failure, output a message
	if s.postFailureActionCount > 0 {
		s.requestLine()
		if s.postFailureActionCount == 1 {
			s.print(fmt.Sprintf("There was 1 action that completed after the action that failed. See verbose.log.gz for its output."))
		} else {
			s.print(fmt.Sprintf("There were %d actions that completed after the action that failed. See verbose.log.gz for their output.", s.postFailureActionCount))
		}
	}

	s.requestLine()

	// Clear the running actions list
	s.runningActions = nil

	if s.tableMode {
		// Update table to clear the display
		s.actionTable()

		// Reset scrolling margins to cover the entire terminal
		fmt.Fprintf(s.writer, ansi.resetScrollingMargins())
		_, height, _ := termSize(s.writer)
		// Move cursor to where the top of the non-scrolling region was (now blank)
		fmt.Fprintf(s.writer, ansi.setCursor(height-s.tableHeight, 1))
		// Show the cursor again
		fmt.Fprintf(s.writer, ansi.showCursor())
	}
}

// Write implements the io.Writer interface for direct output writing.
// This allows the smartStatusOutput to be used as an output destination
// for command stdout/stderr redirection.
//
// Parameters:
//   - p: The byte slice to write
//
// Returns:
//   - The number of bytes written (always len(p))
//   - An error (always nil, as this implementation never fails)
//
// How it works:
//   - Acquires the lock to ensure thread-safe access to haveBlankLine
//   - Calls print() to output the string, which handles blank line tracking
//     and newline management
//   - Returns the length of the input slice and nil error
//
// Note: This is used to capture command output (stdout) that should be
// displayed in the scrolling region of the terminal.
func (s *smartStatusOutput) Write(p []byte) (int, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.print(string(p))
	return len(p), nil
}

// requestLine requests outputting a blank line.
// If there is currently no blank line (haveBlankLine is false), outputs a newline character.
// This is used to add separation between output content.
//
// How it works:
//   - Checks the haveBlankLine flag which tracks whether the last output ended with a newline
//   - If no blank line exists, prints a newline and sets haveBlankLine to true
//   - This ensures proper spacing between different output blocks
func (s *smartStatusOutput) requestLine() {
	if !s.haveBlankLine {
		fmt.Fprintln(s.writer)
		s.haveBlankLine = true
	}
}

// print prints a string to the output.
// If there is currently no blank line (meaning we're in the middle of a line),
// first clears the current line before printing the new content.
//
// Parameters:
//   - str: The string to print
//
// How it works:
//  1. If there is no blank line (haveBlankLine is false):
//     - Moves cursor to beginning of line with \r
//     - Clears from cursor to end of line using ANSI escape sequence
//     - Sets haveBlankLine to true
//  2. Prints the string
//  3. If the string doesn't end with a newline, adds one
//     (unless the string is empty)
//
// The line clearing behavior ensures that when outputting to the middle of
// an existing line (like when overwriting a status line), the old content is
// properly erased.
func (s *smartStatusOutput) print(str string) {
	if !s.haveBlankLine {
		fmt.Fprint(s.writer, "\r", ansi.clearToEndOfLine())
		s.haveBlankLine = true
	}
	fmt.Fprint(s.writer, str)
	if len(str) == 0 || str[len(str)-1] != '\n' {
		fmt.Fprint(s.writer, "\n")
	}
}

// statusLine displays information on the terminal status line.
// The status line is displayed at the bottom of the terminal (in the non-scrolling
// region when table mode is active) and shows current operation or progress information.
//
// Parameters:
//   - str: The string to display on the status line
//
// How it works:
//  1. If the string contains newlines, only takes the first line (ignores continuation)
//  2. Truncates the string to fit the terminal width (handling ANSI escape sequences)
//  3. Adds carriage return to move cursor to beginning of line
//  4. If the string doesn't already contain ANSI escape sequences:
//     - Adds bold blue formatting at the start
//     - Adds regular (reset) formatting at the end
//  5. Clears to end of line to remove any leftover characters from previous content
//  6. Sets haveBlankLine to false (we're on a status line, not a blank line)
//
// The color formatting (bold blue) makes the status line stand out visually.
// The clearing ensures that if the new status line is shorter than the previous one,
// the remaining characters from the old line are removed.
func (s *smartStatusOutput) statusLine(str string) {
	// Take only the first line, ignore continuation
	idx := strings.IndexRune(str, '\n')
	if idx != -1 {
		str = str[0:idx]
	}

	// Limit line width to terminal width, otherwise it wraps and can't be deleted properly
	str = elide(str, s.termWidth)

	// If formatter already embedded colors, avoid nesting color sequences
	// Nested color/reset sequences cause different segments of the status line
	// to appear as independently redrawn pieces
	start := "\r"
	end := ansi.clearToEndOfLine()
	if !strings.ContainsRune(str, '\x1b') {
		start += ansi.boldBlue()
		end = ansi.regular() + end
	}
	fmt.Fprint(s.writer, start, str, end)
	s.haveBlankLine = false
}

// elide truncates a string according to the terminal width.
// This function considers ANSI escape sequences when calculating visible character length,
// ensuring that strings with embedded colors are truncated correctly.
//
// Parameters:
//   - str: The string to truncate
//   - width: The maximum visible character width
//
// Returns:
//
//	The truncated string with ANSI reset sequence appended if truncation occurred,
//	or the original string if its visible length is less than or equal to width
//
// How it works:
//  1. If width is <= 0, returns the original string unchanged
//  2. Calculates the visible length by stripping ANSI escape sequences
//  3. If visible length <= width, returns the original string
//  4. If truncation is needed:
//     - Iterates through the string character by character
//     - Tracks whether we're inside an ANSI escape sequence (starting with \x1b)
//     - For ANSI sequences, looks for alphabetic characters that end the sequence
//     - For visible characters, increments the visible count
//     - When visible count reaches width, truncates at that position
//     - Appends ANSI regular (reset) sequence to clear any color formatting
//
// Example:
//
//	If str = "\x1b[31mError\x1b[0m" (red "Error") and width = 3,
//	the visible "Error" would be truncated to "Err" with reset sequence added.
func elide(str string, width int) string {
	if width <= 0 {
		return str
	}
	// Calculate visible length by removing ANSI escape sequences
	visibleLen := len(string(stripAnsiEscapes([]byte(str))))
	if visibleLen <= width {
		return str
	}
	// Need to truncate. Find position in original string corresponding to target visible width
	visibleCount := 0
	inEsc := false
	for i, c := range str {
		if inEsc {
			if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
				inEsc = false
			}
			continue
		}
		if c == '\x1b' {
			inEsc = true
			continue
		}
		visibleCount++
		if visibleCount >= width {
			return str[:i+1] + ansi.regular()
		}
	}
	return str
}

// startActionTableTick starts a ticker that updates the action table every second.
// This runs in a separate goroutine that periodically refreshes the display of running actions.
//
// How it works:
//  1. Creates a new ticker that fires every second (time.Second)
//  2. Starts a goroutine with an infinite loop that:
//     - Uses select to wait for either ticker events or the done channel
//     - When ticker fires: acquires lock, calls actionTable() to update display, releases lock
//     - When done channel receives: exits the loop and terminates the goroutine
//
// The ticker runs independently to provide real-time updates of:
// - The running time for each action (updated every second)
// - Any changes in the running actions list
// - Color changes as actions pass the 30-second and 60-second thresholds
//
// Thread Safety:
//
//	The lock ensures that the action table update doesn't conflict with other
//	methods that modify runningActions (like StartAction, FinishAction).
func (s *smartStatusOutput) startActionTableTick() {
	s.ticker = time.NewTicker(time.Second)
	go func() {
		for {
			select {
			case <-s.ticker.C:
				s.lock.Lock()
				s.actionTable()
				s.lock.Unlock()
			case <-s.done:
				return
			}
		}
	}()
}

// stopActionTableTick stops the action table update ticker.
// This is called during Flush to stop the periodic table updates.
//
// How it works:
//  1. Stops the ticker (stops it from generating any more events)
//  2. Sends a signal on the done channel to tell the ticker goroutine to exit
//
// Note: The goroutine started by startActionTableTick is waiting on both
// s.ticker.C and s.done channels. After this method returns, the goroutine
// will receive from s.done and exit. This is why we send the signal before
// returning - it ensures the goroutine will terminate.
func (s *smartStatusOutput) stopActionTableTick() {
	s.ticker.Stop()
	s.done <- true
}

// startSigwinch starts listening for SIGWINCH signals.
// SIGWINCH is sent by the system when the terminal size changes (e.g., user resizes
// the terminal window). Listening for this signal allows real-time updates to the
// table and terminal dimension information.
//
// How it works:
//  1. Registers the sigwinch channel to receive SIGWINCH signals using signal.Notify
//  2. Starts a goroutine that listens on the sigwinch channel
//  3. When SIGWINCH is received:
//     - Acquires the lock to safely update terminal dimensions
//     - Calls updateTermSize() to get new dimensions and adjust table if needed
//     - If table mode is active, redraws the action table
//     - Releases the lock
//     - If sigwinchHandled channel is set, sends a signal to indicate handling complete
//
// The signal handler runs in its own goroutine to avoid blocking the main code.
// Thread Safety: The lock ensures that terminal dimension updates don't conflict
// with other methods that use those dimensions.
func (s *smartStatusOutput) startSigwinch() {
	signal.Notify(s.sigwinch, syscall.SIGWINCH)
	go func() {
		for _ = range s.sigwinch {
			s.lock.Lock()
			s.updateTermSize()
			if s.tableMode {
				s.actionTable()
			}
			s.lock.Unlock()
			if s.sigwinchHandled != nil {
				s.sigwinchHandled <- true
			}
		}
	}()
}

// stopSigwinch stops listening for SIGWINCH signals.
// This is called during Flush to clean up the signal handler.
//
// How it works:
//  1. Calls signal.Stop(s.sigwinch) to stop forwarding SIGWINCH signals to the channel
//  2. Closes the sigwinch channel to clean up resources
//
// After calling this, the signal handler goroutine will exit because it will
// receive from a closed channel (which yields zero values indefinitely).
func (s *smartStatusOutput) stopSigwinch() {
	signal.Stop(s.sigwinch)
	close(s.sigwinch)
}

// computeTableHeight computes the actual table height based on terminal height and requested height.
//
// Calculation logic:
//   - If requestedTableHeight is greater than 0, use that value directly
//   - Otherwise, automatically calculate: terminal height / 4, with minimum of 1 row and maximum of 10 rows
//   - Finally, ensure table height doesn't exceed terminal height minus 1 (leaving at least 1 row for scrolling region)
//
// This ensures the action table doesn't take up too much of the terminal while still
// providing useful information. The 1/4 ratio is a balance between showing enough
// actions and leaving room for output. The 1-10 row clamp prevents extreme sizes.
func (s *smartStatusOutput) computeTableHeight() {
	tableHeight := s.requestedTableHeight
	if tableHeight == 0 {
		tableHeight = s.termHeight / 4
		if tableHeight < 1 {
			tableHeight = 1
		} else if tableHeight > 10 {
			tableHeight = 10
		}
	}
	if tableHeight > s.termHeight-1 {
		tableHeight = s.termHeight - 1
	}
	s.tableHeight = tableHeight
}

// updateTermSize handles terminal size changes after receiving SIGWINCH signal.
// Gets the new terminal size and recalculates table height if table mode is enabled.
// If the scrolling region height changes, attempts to shift existing text to avoid
// it being covered by the table.
//
// Parameters:
//   - oldScrollingHeight: The scrolling region height before the update
//   - scrollingHeight: The scrolling region height after the update
//
// How it works:
//  1. Gets the new terminal size using termSize()
//  2. Records the old scrolling height (termHeight - tableHeight) before update
//  3. Updates termWidth and termHeight with new values
//  4. If table mode is enabled:
//     - Recomputes table height using computeTableHeight()
//     - Calculates new scrolling height
//     - If scrolling height decreased (table grew):
//     - Calculates how many lines to shift down (pan)
//     - Limits the pan to not exceed table height
//     - Uses ANSI panDown to shift existing content down
//
// The panDown call is important because when the table grows, it would otherwise
// overwrite the content that was at the bottom of the scrolling region. Shifting
// that content down makes room for the larger table.
func (s *smartStatusOutput) updateTermSize() {
	if w, h, ok := termSize(s.writer); ok {
		oldScrollingHeight := s.termHeight - s.tableHeight

		s.termWidth, s.termHeight = w, h

		if s.tableMode {
			s.computeTableHeight()

			scrollingHeight := s.termHeight - s.tableHeight

			// If scrolling region changed, try to shift existing text to avoid being covered by table
			if scrollingHeight < oldScrollingHeight {
				pan := oldScrollingHeight - scrollingHeight
				if pan > s.tableHeight {
					pan = s.tableHeight
				}
				fmt.Fprint(s.writer, ansi.panDown(pan))
			}
		}
	}
}

// actionTable draws the action table on the terminal.
// The table is displayed in the non-scrolling region at the bottom of the terminal,
// showing all currently running actions in real-time.
//
// Each table row displays:
//   - Running duration in MM:SS format (minutes:seconds)
//   - Action description (or full command if verbose mode is enabled)
//
// Color coding based on running time:
//   - Normal (white/default): Less than 30 seconds
//   - Yellow + bold: 30-60 seconds (action is taking longer than expected)
//   - Red + bold: More than 60 seconds (action is taking very long)
//
// How it works:
//  1. Calculates the scrolling region height (terminal height - table height)
//  2. Sets the scrolling margins to define the scrolling and non-scrolling regions
//     - Lines 1 to scrollingHeight: scrolling region (for command output)
//     - Lines scrollingHeight+1 to terminalHeight: non-scrolling region (for action table)
//  3. For each row in the table height:
//     - Moves cursor to the appropriate line in the non-scrolling region
//     - If there's a running action for this row:
//     - Calculates elapsed seconds since action started
//     - Gets action description (prefers Description, or Command, or full command in verbose mode)
//     - Determines color based on elapsed time (yellow for 30s+, red for 60s+)
//     - Formats duration string as "   M:SS"
//     - Truncates description to fit remaining terminal width
//     - Combines color, duration, and description for output
//     - Clears to end of line to remove any leftover content from previous display
//  4. Finally moves cursor back to the last line of the scrolling region
//     (so subsequent output appears in the right place)
//
// The scrolling margins ensure that when users scroll through command output,
// the action table stays fixed at the bottom and doesn't scroll out of view.
func (s *smartStatusOutput) actionTable() {
	scrollingHeight := s.termHeight - s.tableHeight

	// Update scrolling margins in case terminal height changed
	fmt.Fprint(s.writer, ansi.setScrollingMargins(1, scrollingHeight))

	// Write status lines suitable for the table height
	for tableLine := 0; tableLine < s.tableHeight; tableLine++ {
		if tableLine >= s.tableHeight {
			break
		}
		// Move cursor to the correct line in the non-scrolling region
		fmt.Fprint(s.writer, ansi.setCursor(scrollingHeight+1+tableLine, 1))

		if tableLine < len(s.runningActions) {
			runningAction := s.runningActions[tableLine]

			// Calculate seconds elapsed
			seconds := int(time.Since(runningAction.startTime).Round(time.Second).Seconds())

			// Get action description
			desc := runningAction.action.Description
			if s.verbose {
				// In verbose mode, show full command
				if runningAction.action.Command != "" {
					desc = runningAction.action.Command
				}
			} else if desc == "" {
				// In concise mode, show command if no description
				desc = runningAction.action.Command
			}

			// Set color based on running time
			color := ""
			if seconds >= 60 {
				// Over 60 seconds: red + bold
				color = ansi.red() + ansi.bold()
			} else if seconds >= 30 {
				// 30-60 seconds: yellow + bold
				color = ansi.yellow() + ansi.bold()
			}

			// Format duration string as "   M:SS "
			durationStr := fmt.Sprintf("   %2d:%02d ", seconds/60, seconds%60)
			// Truncate description based on terminal width
			desc = elide(desc, s.termWidth-len(durationStr))
			// Combine color and duration
			durationStr = color + durationStr + ansi.regular()
			fmt.Fprint(s.writer, durationStr, desc)
		}
		// Clear to end of line to remove any leftover content
		fmt.Fprint(s.writer, ansi.clearToEndOfLine())
	}

	// Move cursor back to the last line of the scrolling region
	fmt.Fprintf(s.writer, ansi.setCursor(scrollingHeight, 1))
}

// ansi is a singleton instance of the ANSI escape sequence generator.
// This instance is used throughout smartStatusOutput to generate various
// terminal control sequences for colors, cursor positioning, scrolling, etc.
//
// ANSI escape sequences are a standard for terminal control that began with
// the ANSI standard. They typically start with the escape character (\x1b or ESC)
// followed by [ and then various parameters and a letter designating the command.
var ansi = ansiImpl{}

// ansiImpl is the implementation of ANSI escape sequence generation.
// This struct contains methods that return strings containing ANSI control
// sequences for various terminal operations.
//
// These methods generate escape sequences following the ANSI/ECMA-48 standard:
// - CSI (Control Sequence Introducer) sequences start with ESC[
// - The sequence ends with a letter specifying the command
type ansiImpl struct{}

// clearToEndOfLine clears all characters from the cursor position to the end of the current line.
// This is useful for overwriting content with shorter new content, ensuring no leftover characters.
//
// ANSI escape sequence: ESC[K
//   - ESC: \x1b
//   - [ : Introduces a CSI sequence
//   - K: Erase in Line (EL) command
//   - 0 parameter (default): Clear from cursor to end of line
//
// Equivalent to: \x1b[K
//
// Example use: When updating the status line with shorter text, this clears
// any remaining characters from the previous longer status line.
func (ansiImpl) clearToEndOfLine() string {
	return "\x1b[K"
}

// setCursor moves the cursor to the specified position.
// This is used for positioning the cursor before writing content, particularly
// when updating the action table or moving to specific locations.
//
// ANSI escape sequence: ESC[row;columnH
//   - ESC: \x1b
//   - [ : CSI introducer
//   - row: The target row number (1-indexed, top is 1)
//   - ; : Separator between parameters
//   - column: The target column number (1-indexed, left is 1)
//   - H: Cursor Position (CUP) command
//
// Equivalent to: \x1b[row;columnH
//
// Example: \x1b[5;10H moves cursor to row 5, column 10
//
// Note: This is the "Direct Cursor Address" command - row and column are
// both 1-indexed, with (1,1) being the top-left corner.
func (ansiImpl) setCursor(row, column int) string {
	// Direct cursor address
	return fmt.Sprintf("\x1b[%d;%dH", row, column)
}

// setScrollingMargins sets the terminal's scrolling region margins.
// This divides the terminal into two regions:
// - Lines top to bottom-1: The scrolling region where content scrolls normally
// - Lines bottom to terminalHeight: The non-scrolling region (framing area)
//
// The scrolling region is used for command output and normal text display,
// while the non-scrolling region stays fixed and is used for the action table.
//
// ANSI escape sequence: ESC[top;bottomr
//   - ESC: \x1b
//   - [ : CSI introducer
//   - top: Top margin line number (1-indexed)
//   - ; : Parameter separator
//   - bottom: Bottom margin line number (1-indexed)
//   - r: DECSTBM (Set Top and Bottom Margins) command
//
// Equivalent to: \x1b[top;bottomr
//
// Example: \x1b[1;20r on a 25-line terminal sets:
// - Lines 1-20: Scrolling region
// - Lines 21-25: Non-scrolling region (fixed)
//
// This is crucial for keeping the action table visible at the bottom
// while command output scrolls in the upper area.
func (ansiImpl) setScrollingMargins(top, bottom int) string {
	// Set Top and Bottom Margins DECSTBM
	return fmt.Sprintf("\x1b[%d;%dr", top, bottom)
}

// resetScrollingMargins resets the scrolling margins to cover the entire terminal.
// This is called during Flush to restore normal terminal behavior where
// the entire screen scrolls as a single unit.
//
// ANSI escape sequence: ESC[r
//   - ESC: \x1b
//   - [ : CSI introducer
//   - r: DECSTBM with no parameters - resets to full screen
//
// Equivalent to: \x1b[r
//
// After this call, the entire terminal becomes a single scrolling region,
// allowing normal terminal behavior.
func (ansiImpl) resetScrollingMargins() string {
	// Set Top and Bottom Margins DECSTBM
	return fmt.Sprintf("\x1b[r")
}

// red returns the ANSI escape sequence for red text color.
// This is used to highlight actions that have been running for more than 60 seconds,
// indicating they are taking very long.
//
// ANSI escape sequence: ESC[31m
//   - ESC: \x1b
//   - [ : CSI introducer
//   - 31: Foreground color code for red
//   - m: SGR (Select Graphic Rendition) command
//
// Equivalent to: \x1b[31m
//
// The color codes are:
//   - 30: Black
//   - 31: Red
//   - 32: Green
//   - 33: Yellow
//   - 34: Blue
//   - 35: Magenta
//   - 36: Cyan
//   - 37: White
func (ansiImpl) red() string {
	return "\x1b[31m"
}

// yellow returns the ANSI escape sequence for yellow text color.
// This is used to highlight actions that have been running for 30-60 seconds,
// indicating they are taking longer than expected.
//
// ANSI escape sequence: ESC[33m
//   - ESC: \x1b
//   - [ : CSI introducer
//   - 33: Foreground color code for yellow
//   - m: SGR command
//
// Equivalent to: \x1b[33m
func (ansiImpl) yellow() string {
	return "\x1b[33m"
}

// bold returns the ANSI escape sequence for bold/strong text formatting.
// This is combined with colors to make important information stand out more.
//
// ANSI escape sequence: ESC[1m
//   - ESC: \x1b
//   - [ : CSI introducer
//   - 1: Bold intensity
//   - m: SGR command
//
// Equivalent to: \x1b[1m
//
// Other intensity values:
//   - 1: Bold
//   - 2: Dim
//   - 3: Italic
//   - 4: Underline
//   - 5: Blink (slow)
//   - 7: Reverse
//   - 9: Strikethrough
func (ansiImpl) bold() string {
	return "\x1b[1m"
}

// boldBlue returns the ANSI escape sequence for bright blue bold text.
// This is used for the status line to make it visually prominent.
//
// ANSI escape sequence: ESC[94m
//   - ESC: \x1b
//   - [ : CSI introducer
//   - 94: Bright blue foreground color (8-bit color, values 90-97 are bright variants)
//   - m: SGR command
//
// Equivalent to: \x1b[94m
//
// The 8-bit color extension:
//   - 90-97: Bright colors (black, red, green, yellow, blue, magenta, cyan, white)
//   - 38;5;n: 256-color mode where n is 0-255
//   - 38;2;r;g;b: True color mode with RGB values
func (ansiImpl) boldBlue() string {
	return "\x1b[94m"
}

// boldGreen returns the ANSI escape sequence for bright green bold text.
// This could be used for success messages or completed actions.
//
// ANSI escape sequence: ESC[92m
//   - ESC: \x1b
//   - [ : CSI introducer
//   - 92: Bright green foreground color
//   - m: SGR command
//
// Equivalent to: \x1b[92m
func (ansiImpl) boldGreen() string {
	return "\x1b[92m"
}

// brightWhite returns the ANSI escape sequence for bright white text.
// This could be used for high-visibility text.
//
// ANSI escape sequence: ESC[1;97m
//   - ESC: \x1b
//   - [ : CSI introducer
//   - 1: Bold attribute
//   - ; : Parameter separator
//   - 97: Bright white foreground color
//   - m: SGR command
//
// Equivalent to: \x1b[1;97m
func (ansiImpl) brightWhite() string {
	return "\x1b[1;97m"
}

// regular returns the ANSI escape sequence that resets all text attributes
// to their default state. This turns off all colors, bold, italic, and other
// formatting, returning to the terminal's default text appearance.
//
// ANSI escape sequence: ESC[0m
//   - ESC: \x1b
//   - [ : CSI introducer
//   - 0: Reset all attributes
//   - m: SGR command
//
// Equivalent to: \x1b[0m
//
// This is essential to call after using any color or formatting sequences,
// otherwise subsequent text would inherit the previous formatting. Every
// colored/formatted string should be followed by this reset sequence.
//
// Common reset values:
//   - 0: Reset all attributes (most common)
//   - 1: Reset bold
//   - 22: Reset bold/dim
//   - 23: Reset italic
//   - 24: Reset underline
//   - 27: Reset reverse
//   - 39: Reset foreground color to default
//   - 49: Reset background color to default
func (ansiImpl) regular() string {
	return "\x1b[0m"
}

// showCursor makes the terminal cursor visible.
// This is called during Flush to restore normal cursor visibility after
// the action table display was hidden.
//
// ANSI escape sequence: ESC[?25h
//   - ESC: \x1b
//   - [ : CSI introducer
//   - ? : Indicates a private/extension sequence (DEC private mode)
//   - 25: DEC private mode number for cursor visibility
//   - h: DECSET (Set Mode) command - turns the mode on
//
// Equivalent to: \x1b[?25h
//
// DEC mode numbers:
//   - ?25: Cursor visibility (h=show, l=hide)
//
// This is part of the DEC VT series escape sequences, hence the ? prefix.
func (ansiImpl) showCursor() string {
	return "\x1b[?25h"
}

// hideCursor hides the terminal cursor.
// This is called when table mode is initialized to avoid seeing the cursor
// jump around during the frequent display updates. A visible cursor during
// the rapid updates would be visually distracting.
//
// ANSI escape sequence: ESC[?25l
//   - ESC: \x1b
//   - [ : CSI introducer
//   - ? : DEC private mode indicator
//   - 25: Cursor visibility mode
//   - l: DECRST (Reset Mode) command - turns the mode off
//
// Equivalent to: \x1b[?25l
//
// Note: l (lowercase L) is the reset variant, h is the set variant.
// This uses DECTCEM (Text Cursor Enable Mode) - when disabled, the cursor
// is hidden.
func (ansiImpl) hideCursor() string {
	return "\x1b[?25l"
}

// panDown scrolls the terminal content downward by the specified number of lines.
// This is used when the action table height increases (e.g., terminal gets shorter)
// to shift existing content down so it doesn't get covered by the table.
//
// Parameters:
//   - lines: The number of lines to scroll down
//
// ANSI escape sequence: ESC[linesS
//   - ESC: \x1b
//   - [ : CSI introducer
//   - lines: Number of lines to scroll
//   - S: SD (Scroll Down) command
//
// Equivalent to: \x1b[linesS
//
// How it works:
//   - The content in the scrolling region moves down by the specified lines
//   - New blank lines appear at the top of the scrolling region
//   - Lines that scroll past the bottom of the scrolling region are lost
//
// This is the inverse of panUp which scrolls content up.
// Used in updateTermSize() when the table grows to prevent overwriting
// existing output that was near the bottom of the screen.
func (ansiImpl) panDown(lines int) string {
	return fmt.Sprintf("\x1b[%dS", lines)
}

// panUp scrolls the terminal content upward by the specified number of lines.
// This could be used if the action table shrinks to pull content back up.
//
// Parameters:
//   - lines: The number of lines to scroll up
//
// ANSI escape sequence: ESC[linesT
//   - ESC: \x1b
//   - [ : CSI introducer
//   - lines: Number of lines to scroll
//   - T: SU (Scroll Up) command
//
// Equivalent to: \x1b[linesT
//
// How it works:
//   - The content in the scrolling region moves up by the specified lines
//   - New blank lines appear at the bottom of the scrolling region
//   - Lines that scroll past the top of the scrolling region are lost
//
// This is the inverse of panDown.
func (ansiImpl) panUp(lines int) string {
	return fmt.Sprintf("\x1b[%dT", lines)
}
