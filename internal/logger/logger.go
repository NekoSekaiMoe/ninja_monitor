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

// Package logger provides a simple logging interface for ninja_monitor.
package logger

import (
	"fmt"
	"os"
)

// Logger is a simple logging interface.
type Logger interface {
	Fatalf(format string, args ...any)
	Error(msg string)
	Verbose(msg string)
	Print(msg string)
	Status(msg string)
}

// SimpleLogger is a basic implementation of Logger that writes to stderr.
type SimpleLogger struct {
	verbose bool
}

// NewSimpleLogger creates a new SimpleLogger.
func NewSimpleLogger(verbose bool) *SimpleLogger {
	return &SimpleLogger{verbose: verbose}
}

func (l *SimpleLogger) Fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", args...)
	os.Exit(1)
}

func (l *SimpleLogger) Error(msg string) {
	fmt.Fprintln(os.Stderr, "ERROR: "+msg)
}

func (l *SimpleLogger) Errorf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ERROR: "+format+"\n", args...)
}

func (l *SimpleLogger) Verbose(msg string) {
	if l.verbose {
		fmt.Fprintln(os.Stderr, "VERBOSE: "+msg)
	}
}

func (l *SimpleLogger) Print(msg string) {
	fmt.Fprintln(os.Stderr, msg)
}

func (l *SimpleLogger) Status(msg string) {
	fmt.Fprintln(os.Stderr, msg)
}
