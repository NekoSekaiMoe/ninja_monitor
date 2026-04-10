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

var (
	fifoPath    = flag.String("fifo", "", "Path to FIFO for monitor-only mode (if empty, auto-generates)")
	formatStr   = flag.String("format", "", "Progress format string (NINJA_STATUS style)")
	verbose     = flag.Bool("v", false, "Verbose output")
	quiet       = flag.Bool("quiet", false, "Quiet mode (no smart status table)")
	tableHeight = flag.Int("table-height", 0, "Height of action table (0=auto, negative=disable)")
	ninjaPath   = flag.String("ninja", "", "Path to ninja executable (default: same dir as ninja_monitor)")
)

// findNinja returns the path to ninja executable.
// If ninjaPath is specified, returns it directly.
// Otherwise, looks for "ninja" in the same directory as the current executable.
func findNinja(ninjaPath string) (string, error) {
	if ninjaPath != "" {
		return ninjaPath, nil
	}

	// Get the directory of the current executable
	execPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to get executable path: %v", err)
	}

	execDir := filepath.Dir(execPath)
	ninjaInSameDir := filepath.Join(execDir, "ninja_mod")

	// Check if ninja exists in the same directory
	if _, err := os.Stat(ninjaInSameDir); err == nil {
		return ninjaInSameDir, nil
	}

	// Fallback: look for "ninja" in PATH
	if ninjaInPath, err := exec.LookPath("ninja"); err == nil {
		return ninjaInPath, nil
	}

	return "", fmt.Errorf("ninja not found in %s or in PATH", execDir)
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: ninja_monitor [options] [-- ninja_args...]\n")
		fmt.Fprintf(os.Stderr, "       ninja_monitor --fifo=/path/to/fifo  # Monitor-only mode\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nFormat placeholders:\n")
		fmt.Fprintf(os.Stderr, "  %%s - started actions, %%t - total actions, %%r - running actions\n")
		fmt.Fprintf(os.Stderr, "  %%f - finished actions, %%p - percentage, %%e - elapsed time\n")
		fmt.Fprintf(os.Stderr, "  %%l - remaining time estimate\n")
	}

	flag.Parse()

	args := flag.Args()

	// Determine mode
	monitorOnly := *fifoPath != ""
	var ninjaArgs []string

	if !monitorOnly {
		// Check if there are ninja arguments after --
		sepIndex := -1
		for i, arg := range args {
			if arg == "--" {
				sepIndex = i
				break
			}
		}

		if sepIndex >= 0 {
			ninjaArgs = args[sepIndex+1:]
		} else if len(args) > 0 {
			// Treat all args as ninja args
			ninjaArgs = args
		}
	}

	log := logger.NewSimpleLogger(*verbose)

	// Find ninja executable
	ninja, err := findNinja(*ninjaPath)
	if err != nil {
		log.Fatalf("Failed to find ninja: %v", err)
	}
	log.Verbose(fmt.Sprintf("Using ninja: %s", ninja))

	// Create FIFO path if not specified
	fifo := *fifoPath
	if fifo == "" {
		fifo = "/tmp/.ninja_fifo"
	}

	// Create status multiplexer
	st := &status.Status{}

	// Create status output
	colorize := terminal.IsSmartTerminal(os.Stdout)
	formatter := terminal.NewFormatter(colorize, *formatStr, *quiet)

	var output status.StatusOutput
	if !*quiet && colorize {
		output = terminal.NewSmartStatusOutputWithHeight(os.Stdout, formatter, *tableHeight, *verbose)
	} else {
		output = terminal.NewSimpleStatusOutput(os.Stdout, formatter, false)
	}
	st.AddOutput(output)

	// Create ninja reader
	toolStatus := st.StartTool()
	nr := status.NewNinjaReader(log, toolStatus, fifo)
	defer nr.Close()

	// Track if we received an interrupt signal
	interrupted := false
	exitCode := 0

	// If not monitor-only mode, start ninja
	var ninjaCmd *exec.Cmd
	if !monitorOnly {
		// Build ninja command
		fullArgs := []string{
			// Enable time estimation using .ninja_log history
			"-o", "usesninjalogasweightlist=yes",
			"--frontend_file", fifo,
		}
		fullArgs = append(fullArgs, ninjaArgs...)

		ninjaCmd = exec.Command(ninja, fullArgs...)
		ninjaCmd.Stdout = os.Stdout
		ninjaCmd.Stderr = os.Stderr
		ninjaCmd.Stdin = os.Stdin

		if err := ninjaCmd.Start(); err != nil {
			log.Fatalf("Failed to start ninja: %v", err)
		}
	}

	// Handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		interrupted = true
		if ninjaCmd != nil {
			// Forward the signal to ninja
			ninjaCmd.Process.Signal(syscall.SIGINT)
		}
	}()

	// Wait for ninja to finish
	if ninjaCmd != nil {
		err := ninjaCmd.Wait()
		if err != nil {
			if interrupted {
				// User interrupted, exit with 130 (128 + SIGINT)
				exitCode = 130
			} else if exitErr, ok := err.(*exec.ExitError); ok {
				// Get the actual exit code from ninja
				if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
					if status.Signaled() {
						// Killed by signal
						exitCode = 128 + int(status.Signal())
					} else {
						exitCode = status.ExitStatus()
					}
				} else {
					exitCode = 1
				}
			} else {
				exitCode = 1
			}
		}
	} else {
		// Monitor-only mode: wait for interrupt
		<-sigChan
		exitCode = 130
	}

	st.Finish()
	os.Exit(exitCode)
}
