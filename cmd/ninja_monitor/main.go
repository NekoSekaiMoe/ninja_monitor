// Copyright 2024 Google Inc. All rights reserved.
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

// Package main is the entry point for the ninja_monitor program.
// ninja_monitor is a tool that monitors ninja build progress in real-time.
// It displays build progress, estimates remaining time, and provides
// beautiful terminal output with smart status tables.
//
// This program supports two running modes:
//   - Full Mode (default): Starts the ninja process and monitors its build progress
//   - Monitor-Only Mode: Only listens to a FIFO pipe without starting ninja (via --fifo flag)
//
// Communication with ninja uses a FIFO (Named Pipe) mechanism:
// - ninja writes build progress information to the FIFO
// - ninja_monitor reads from the FIFO and displays the progress
// - The FIFO path can be specified via --fifo flag or auto-generated
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"ninja_monitor/internal/logger"
	"ninja_monitor/internal/status"
	"ninja_monitor/internal/terminal"
)

// ============================================================================
// Command-Line Flags
// ============================================================================
// These flags control the behavior of ninja_monitor. All flags are optional.
//
// Full Mode Usage:
//   ninja_monitor [options] [-- ninja_args...]
//   Example: ninja_monitor -v -- -C myproject
//
// Monitor-Only Mode Usage:
//   ninja_monitor --fifo=/path/to/fifo
//   Example: ninja_monitor --fifo=/tmp/.ninja_fifo

var (
	// fifoPath specifies the path to the FIFO (Named Pipe) for inter-process communication.
	// In monitor-only mode, this flag must be specified.
	// In full mode, if empty, a default path "/tmp/.ninja_fifo" will be used.
	//
	// FIFO Mechanism for IPC with ninja:
	// - FIFO (Named Pipe) is a special file type used for one-way communication
	// - ninja writes build progress data to the FIFO in a specific format
	// - ninja_monitor reads from the FIFO and parses the progress data
	// - The format includes: started actions, total actions, running actions,
	//   finished actions, percentage complete, elapsed time, estimated remaining time
	//
	// How it works:
	// 1. When ninja starts building, it writes status updates to the FIFO
	// 2. ninja_monitor continuously reads from the FIFO (non-blocking)
	// 3. Each line in FIFO contains progress info (e.g., "1/100 50 10 50 50.0% 5s 10s")
	// 4. ninja_monitor parses each line and updates the display
	//
	// Example FIFO content (one line per build update):
	//   started/total running finished percentage elapsed remaining
	//   "5/100 3 2 5.0% 10s 190s"
	fifoPath = flag.String("fifo", "", "Path to FIFO for monitor-only mode (if empty, auto-generates)")

	// formatStr specifies a custom progress format string, following the same
	// syntax as the NINJA_STATUS environment variable used by ninja.
	//
	// Supported placeholders:
	//   %s - Number of actions that have started (started count)
	//   %t - Total number of actions to complete (total count)
	//   %r - Number of actions currently running (running count)
	//   %f - Number of finished actions (finished count)
	//   %p - Percentage complete (e.g., "50%")
	//   %e - Elapsed time since build started (e.g., "1m30s")
	//   %l - Estimated remaining time (e.g., "2m45s")
	//
	// Example format strings:
	//   "[%f/%t] %e left" -> "[50/100] 1m30s left"
	//   "Building: %r/%t active, %p complete" -> "Building: 3/10 active, 50% complete"
	//   "[%s/%t] %e<%l" -> "[50/100] 1m30s<2m45s"
	//
	// If empty, a default format will be used that shows progress bar.
	formatStr = flag.String("format", "", "Progress format string (NINJA_STATUS style)")

	// verbose enables verbose output mode, showing additional debug information.
	// When enabled, the program will print:
	//   - Detailed information about FIFO creation
	//   - Parse errors and warnings
	//   - Status update statistics
	//   - Timing information for various operations
	//
	// This is useful for debugging or understanding how the program works.
	verbose = flag.Bool("verbose", false, "Verbose output")

	// quiet enables quiet mode, which disables the smart status table output.
	// In quiet mode, only simple progress information is displayed (no action table).
	//
	// Smart Status Table (when not quiet):
	// - Shows a table of all running and pending actions
	// - Each row shows: status icon, action name, progress percentage
	// - Color-coded: green=done, yellow=running, gray=pending
	//
	// Simple Output (when quiet):
	// - Just shows the progress bar and percentage
	// - No per-action details
	// - Suitable for build logs or CI/CD environments
	quiet = flag.Bool("quiet", false, "Quiet mode (no smart status table)")

	// tableHeight specifies the height (number of rows) of the smart status action table.
	// This table shows the list of running and pending build actions.
	//
	// Values:
	//   0 (default) - Auto-calculate height based on terminal size
	//   positive    - Fixed number of rows to display
	//   negative    - Disable the action table entirely
	//
	// This is useful when you want to limit the table size or disable it completely.
	// For example: --table-height=10 shows only 10 actions
	//             --table-height=-1 disables the table
	tableHeight = flag.Int("table-height", 0, "Height of action table (0=auto, negative=disable)")

	// ninjaPath specifies the path to the ninja executable to use for building.
	// If not specified, automatic lookup is performed:
	//   1. First, look for "ninja_mod" in the same directory as ninja_monitor
	//   2. Then, search for "ninja" in the system PATH
	//
	// The "ninja_mod" name is project-specific (a modified version of ninja).
	// The search order allows flexibility in which ninja version is used.
	//
	// Example:
	//   --ninja=/usr/local/bin/ninja
	//   --ninja=./ninja_mod
	ninjaPath = flag.String("ninja", "", "Path to ninja executable (default: same dir as ninja_monitor)")
)

// ============================================================================
// Function Definitions
// ============================================================================

// findNinja locates and returns the path to the ninja executable.
//
// Search Order:
//  1. If user provides a path via --ninja flag, use that path directly
//  2. Look for "ninja_mod" in the same directory as ninja_monitor executable
//  3. Search for "ninja" in the system PATH using exec.LookPath
//  4. If none found, return an error
//
// Why "ninja_mod"?
// The project uses a modified version of ninja called "ninja_mod" that includes
// additional features for progress reporting via FIFO. This modified version
// writes build progress to the FIFO pipe.
//
// Parameters:
//   - ninjaPath: User-specified ninja path (empty string means auto-detect)
//
// Returns:
//   - string: The found path to ninja executable
//   - error: nil if found, error if not found
//
// How the Lookup Works:
//   - os.Executable() returns the absolute path of the current executable
//   - filepath.Dir() extracts the directory containing the executable
//   - os.Stat() checks if the file exists
//   - exec.LookPath() searches the PATH environment variable
func findNinja(ninjaPath string) (string, error) {
	// If user specified a path, use it directly without verification
	// This allows user to override the automatic lookup
	if ninjaPath != "" {
		return ninjaPath, nil
	}

	// Get the full path of the currently running executable
	// This is needed to find ninja_mod in the same directory
	execPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to get executable path: %v", err)
	}

	// Extract the directory containing ninja_monitor
	execDir := filepath.Dir(execPath)

	// First, try to find "ninja_mod" in the same directory as ninja_monitor
	// This is the project's modified version of ninja
	ninjaInSameDir := filepath.Join(execDir, "ninja_mod")

	// Check if ninja_mod exists in the same directory
	if _, err := os.Stat(ninjaInSameDir); err == nil {
		return ninjaInSameDir, nil
	}

	// Fallback: search for "ninja" in the system PATH
	// This finds the standard ninja installation
	if ninjaInPath, err := exec.LookPath("ninja"); err == nil {
		return ninjaInPath, nil
	}

	// None found - return descriptive error
	return "", fmt.Errorf("ninja not found in %s or in PATH", execDir)
}

// main is the entry point of the ninja_monitor program.
//
// Program Flow:
//  1. Parse command-line flags
//  2. Determine run mode (full mode vs monitor-only mode)
//  3. Locate the ninja executable
//  4. Set up FIFO for IPC (create if needed)
//  5. Initialize status output (smart table or simple text)
//  6. Start the ninja reader (reads FIFO for build progress)
//  7. Set up signal handling (handle SIGINT/SIGTERM)
//  8. Start ninja process (full mode only)
//  9. Wait for ninja to complete
//  10. Set exit code based on ninja's exit status
//  11. Exit with appropriate code
//
// Two Running Modes:
//
// FULL MODE (default):
// - This is the default mode when --fifo is not specified
// - ninja_monitor starts the ninja process as a child process
// - It passes the FIFO path to ninja via --frontend_file flag
// - It monitors ninja's build progress via the FIFO
// - When ninja completes, ninja_monitor exits with ninja's exit code
// - Example: ninja_monitor -v -- -C myproject
//
// MONITOR-ONLY MODE:
// - This mode is enabled by specifying --fifo flag
// - ninja_monitor does NOT start ninja
// - It only listens to the specified FIFO for progress updates
// - Useful when ninja is started by another process or tool
// - Example: ninja_monitor --fifo=/tmp/.ninja_fifo
//
// Command-Line Argument Parsing:
//   - Arguments before "--" are for ninja_monitor
//   - Arguments after "--" are passed to ninja
//   - If no "--" is found, all arguments are passed to ninja
//   - Example: ninja_monitor -v -- -C project build
//     -v is for ninja_monitor
//     -C project build are passed to ninja
//
// Signal Handling:
// - SIGINT (Ctrl+C): User pressed Ctrl+C to interrupt
// - SIGTERM: System admin sent termination signal
// - When received, the signal is forwarded to ninja for graceful shutdown
// - This allows ninja to clean up and exit properly
//
// Exit Code Logic:
// - 0: Build completed successfully
// - 1: Error (ninja not found, failed to start, etc.)
// - 130: Interrupted by user (Ctrl+C) - 128 + SIGINT(2)
// - 143: Terminated by signal - 128 + SIGTERM(15)
// - Other: Exit code from ninja process
func main() {
	// Set custom help message format
	// This displays when user runs --help or provides invalid flags
	flag.Usage = func() {
		// Print usage information
		fmt.Fprintf(os.Stderr, "Usage: ninja_monitor [options] [-- ninja_args...]\n")
		fmt.Fprintf(os.Stderr, "       ninja_monitor --fifo=/path/to/fifo  # Monitor-only mode\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()

		// Print format placeholder explanation
		fmt.Fprintf(os.Stderr, "\nFormat placeholders:\n")
		fmt.Fprintf(os.Stderr, "  %%s - started actions, %%t - total actions, %%r - running actions\n")
		fmt.Fprintf(os.Stderr, "  %%f - finished actions, %%p - percentage, %%e - elapsed time\n")
		fmt.Fprintf(os.Stderr, "  %%l - remaining time estimate\n")
	}

	// Parse all command-line flags
	// This populates the flag variables (fifoPath, verbose, etc.)
	flag.Parse()

	// Get remaining arguments (after flags)
	// These are either ninja arguments or empty
	args := flag.Args()

	// Determine run mode based on --fifo flag
	// If --fifo is specified, use monitor-only mode
	// If --fifo is empty, use full mode (start ninja ourselves)
	monitorOnly := *fifoPath != ""
	var ninjaArgs []string

	// In full mode, separate ninja_monitor args from ninja args
	// The "--" separator divides the two sets of arguments
	if !monitorOnly {
		// Find the "--" separator in arguments
		// "--" is used to separate ninja_monitor options from ninja options
		sepIndex := -1
		for i, arg := range args {
			if arg == "--" {
				sepIndex = i
				break
			}
		}

		// If "--" found, everything after it is ninja arguments
		if sepIndex >= 0 {
			ninjaArgs = args[sepIndex+1:]
		} else if len(args) > 0 {
			// No "--" found, all arguments are for ninja
			// This is common when wrapping ninja_monitor around ninja
			ninjaArgs = args
		}
	}

	// Create a logger for verbose output
	// The logger prints debug information when verbose mode is enabled
	log := logger.NewSimpleLogger(*verbose)

	// Find the ninja executable
	// This searches for ninja_mod or ninja as described in findNinja
	ninja, err := findNinja(*ninjaPath)
	if err != nil {
		// Fatal error - print message and exit with code 1
		log.Fatalf("Failed to find ninja: %v", err)
	}
	log.Verbose(fmt.Sprintf("Using ninja: %s", ninja))

	// Set up FIFO path
	// The FIFO is the communication channel between ninja and ninja_monitor
	// In full mode: ninja writes progress here
	// In monitor-only mode: we read progress from here
	fifo := *fifoPath
	if fifo == "" {
		// Default FIFO path if not specified
		// This is used in full mode
		fifo = "/tmp/.ninja_fifo"
	}

	// Create status manager
	// The status manager collects build progress from various sources
	// and distributes it to output handlers
	st := &status.Status{}

	// Create status formatter
	// The formatter converts raw status data into displayable strings
	// It handles color codes, progress format strings, etc.
	colorize := terminal.IsSmartTerminal(os.Stdout)
	// Create formatter with configuration
	formatter := terminal.NewFormatter(colorize, *formatStr, *quiet, *verbose)

	// Create status output handler
	// The output handler displays the status to the user
	// There are two types:
	//   - SmartStatusOutput: Shows detailed action table with colors
	//   - SimpleStatusOutput: Shows basic progress only
	var output status.StatusOutput
	if !*quiet && colorize {
		// Use smart output for capable terminals
		// Shows action table with per-action details
		output = terminal.NewSmartStatusOutputWithHeight(os.Stdout, formatter, *tableHeight, *verbose)
	} else {
		// Use simple output for non-capable terminals or quiet mode
		// Shows only progress bar/percentage
		output = terminal.NewSimpleStatusOutput(os.Stdout, formatter, false, *verbose)
	}
	// Register the output handler with status manager
	st.AddOutput(output)

	// Create ninja reader
	// The reader reads build progress from the FIFO pipe
	// It parses the FIFO output and updates the status manager
	toolStatus := st.StartTool()                       // Create a tool status tracker
	nr := status.NewNinjaReader(log, toolStatus, fifo) // Create the FIFO reader
	defer nr.Close()                                   // Ensure FIFO is closed when program exits

	// Track interrupt state and exit code
	// These are used for signal handling and exit code determination
	interrupted := false // Flag: did we receive interrupt signal?
	exitCode := 0        // Default exit code (success)

	// In full mode, start ninja as child process
	var ninjaCmd *exec.Cmd
	if !monitorOnly {
		// Construct ninja command arguments
		// -o usesninjalogasweightlist=yes:
		//   Enable time estimation feature
		//   Uses .ninja_log history file to estimate remaining build time
		//   This makes the %l (remaining time) placeholder work
		// --frontend_file:
		//   Specify FIFO path for progress communication
		//   ninja writes progress data to this FIFO
		fullArgs := []string{
			"-o", "usesninjalogasweightlist=yes",
			"--frontend_file", fifo,
		}
		// Append user-provided ninja arguments
		// These come after the "--" separator
		fullArgs = append(fullArgs, ninjaArgs...)

		// Create command object
		// This will execute ninja with the specified arguments
		ninjaCmd = exec.Command(ninja, fullArgs...)

		// Set up I/O for the ninja process
		// Forward stdin, stdout, stderr to terminal
		// This allows user to interact with ninja normally
		ninjaCmd.Stdout = os.Stdout
		ninjaCmd.Stderr = os.Stderr
		ninjaCmd.Stdin = os.Stdin

		// Start the ninja process
		// Note: Start() launches the process but doesn't wait for it
		// The process runs concurrently with ninja_monitor
		if err := ninjaCmd.Start(); err != nil {
			log.Fatalf("Failed to start ninja: %v", err)
		}
	}

	// =========================================================================
	// Signal Handling
	// =========================================================================
	// Handle graceful shutdown when user presses Ctrl+C or sends SIGTERM
	//
	// Signal Handling Flow:
	//  1. Create a signal channel to receive OS signals
	//  2. Subscribe to SIGINT (Ctrl+C) and SIGTERM (kill)
	//  3. Start a goroutine to handle signals asynchronously
	//  4. When signal received:
	//     - Set interrupted flag
	//     - Forward signal to ninja process
	//     - This allows ninja to clean up and exit gracefully
	//
	// Why Forward Signals to Ninja?
	// - Allows ninja to perform cleanup (save state, close files)
	// - Prevents abrupt termination that could leave build artifacts
	// - Ensures .ninja_log is updated for future time estimates

	// Create signal channel with buffer size 1
	// Buffer size 1 prevents signal loss during processing
	sigChan := make(chan os.Signal, 1)

	// Subscribe to termination signals:
	// - SIGINT: Interrupt signal (Ctrl+C)
	//   Sent when user presses Ctrl+C
	//   Default way to stop a running program
	// - SIGTERM: Termination signal (kill command default)
	//   Sent by system admin or process manager
	//   polite way to request process termination
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start signal handler goroutine
	// Run asynchronously to avoid blocking the main thread
	go func() {
		// Wait for any signal to be received
		<-sigChan

		// Mark that we were interrupted
		// This affects exit code calculation
		interrupted = true

		// Forward the signal to ninja process
		// This allows ninja to terminate gracefully
		// Sending SIGINT to ninja:
		//   - Stops currently running build actions
		//   - Saves build state
		//   - Updates .ninja_log for future estimates
		if ninjaCmd != nil {
			ninjaCmd.Process.Signal(syscall.SIGINT)
		}
	}()

	// =========================================================================
	// Wait for Ninja to Complete
	// =========================================================================

	// Wait for ninja process to complete and get its exit status
	if ninjaCmd != nil {
		// Wait for ninja process
		// This blocks until ninja exits
		err := ninjaCmd.Wait()
		if err != nil {
			// ninja exited with an error
			if interrupted {
				// User interrupted with Ctrl+C
				// Use exit code 130 (standard Unix convention)
				// 130 = 128 + SIGINT(2)
				// This indicates the process was interrupted
				exitCode = 130
			} else if exitErr, ok := err.(*exec.ExitError); ok {
				// ninja exited with non-zero status
				// Extract the actual exit code
				if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
					if status.Signaled() {
						// ninja was killed by a signal
						// Calculate exit code: 128 + signal number
						// Example: SIGTERM(15) -> 128 + 15 = 143
						exitCode = 128 + int(status.Signal())
					} else {
						// ninja exited with its own exit code
						// Use that code directly
						exitCode = status.ExitStatus()
					}
				} else {
					// Unable to get status, use generic error code
					exitCode = 1
				}
			} else {
				// Some other error occurred
				exitCode = 1
			}
		}
	} else {
		// Monitor-only mode: wait for user signal
		// In this mode, we don't start ninja
		// We just wait for user to press Ctrl+C
		<-sigChan
		exitCode = 130
	}

	// Notify status manager that build is complete
	// This triggers final output (completion message, statistics)
	st.Finish()

	// Exit with the determined exit code
	// This preserves ninja's exit status for callers
	os.Exit(exitCode)
}
