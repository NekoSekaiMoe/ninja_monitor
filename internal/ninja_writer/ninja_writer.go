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

// Package ninja_writer provides a structured way to generate Ninja build files.
//
// Instead of using string concatenation, callers can construct typed structures
// (Rule, Build, etc.) and serialize them to valid .ninja syntax.
//
// This package handles all the intricacies of Ninja syntax including:
//   - Comments: File-level and inline comments
//   - Variables: Top-level variables and rule/build-scoped variables
//   - Pools: Parallel task limiting mechanisms
//   - Rules: Command templates for building specific file types
//   - Builds: Dependency edges linking inputs to outputs
//   - Defaults: Default targets when running ninja without arguments
//
// Ninja Build System Overview:
// Ninja is a build system that focuses on speed and incremental builds.
// It reads build files (typically named build.ninja) that describe:
//   - Variables: Named values that can be referenced with $name or ${name}
//   - Rules: Named command templates with optional properties
//   - Builds: Dependency edges that specify how to build outputs from inputs
//   - Pools: Limits on parallel execution for resource-intensive tasks
//
// Usage Example:
//
//	nf := ninja_writer.NinjaFile{}
//	nf.AddComment("This is a build file")
//	nf.AddVariable("srcdir", ".")
//	nf.AddRule(ninja_writer.Rule{
//	    Name:    "compile",
//	    Command: "gcc -c $in -o $out",
//	})
//	nf.AddBuild(ninja_writer.Build{
//	    RuleName: "compile",
//	    Inputs:  []string{"main.c"},
//	    Outputs: []string{"main.o"},
//	})
//	nf.WriteFile("build.ninja")
package ninja_writer

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// NinjaFile represents a complete .ninja build file.
//
// This is the central structure of the package. A NinjaFile contains all the
// elements needed to generate a valid Ninja build file:
//
//   - Comments: File-level comments that provide documentation
//   - Variables: Top-level variable definitions visible throughout the file
//   - Pools: Named pools for limiting parallel task execution
//   - Rules: Named command templates that describe how to build files
//   - Builds: Build edges that link inputs to outputs via rules
//   - Defaults: Targets to build when running "ninja" without arguments
//
// When serialized, these elements are written in the order listed above,
// with blank lines separating major sections.
//
// Ninja File Format Overview:
//
// The .ninja format is a simple line-based language:
//
//	# Comments start with hash
//	variable = value
//
//	pool name
//	  depth = N
//
//	rule rulename
//	  command = command to run
//	  description = shown during build
//	  depfile = path to depfile
//	  deps = gcc or msvc
//	  generator = true or false
//	  restat = true or false
//	  varname = value
//
//	build outputs: rule inputs | implicits || orderonly
//	  pool = poolname
//	  varname = value
//
//	default targets
//
// Each section is optional, and elements within sections are ordered by
// their addition to the NinjaFile.
type NinjaFile struct {
	// Comments is a list of file-level comments.
	// Each string represents one comment line.
	// Empty strings produce blank lines in the output.
	// Comments are written at the top of the file with "# " prefix.
	Comments []string

	// Variables is a list of top-level variable definitions.
	// These are global variables visible throughout the entire file.
	// They use the format "name = value" in the output.
	// Top-level variables can be referenced in rules and builds using
	// $name or ${name} syntax.
	Variables []Variable

	// Pools is a list of pool definitions.
	// Pools limit the number of parallel tasks that can run simultaneously.
	// This is useful for resource-intensive operations like linking
	// or operations with other resource constraints.
	// Pool format:
	//   pool poolname
	//     depth = N
	// Where N is the maximum number of parallel tasks.
	Pools []Pool

	// Rules is a list of rule definitions.
	// Rules define command templates that describe how to build specific
	// types of files. They contain the command to execute and various
	// metadata about the build process.
	// Rule format:
	//   rule rulename
	//     command = actual command to run
	//     description = shown during build (optional)
	//     depfile = path to depfile (optional)
	//     deps = gcc or msvc (optional)
	//     generator = true (optional)
	//     restat = true (optional)
	//     varname = value (optional, rule-scoped variables)
	Rules []Rule

	// Builds is a list of build edges.
	// Each build edge specifies how to produce outputs from inputs
	// using a particular rule. This is the core of the build graph.
	// Build format:
	//   build output1 output2: rulename input1 input2 | implicit1 || order1
	//     pool = poolname (optional)
	//     varname = value (optional, build-scoped variables)
	Builds []Build

	// Defaults is a list of default target names.
	// These targets are built when running "ninja" without arguments.
	// Multiple targets can be specified; all will be built.
	// Format: "default target1 target2 target3"
	Defaults []string
}

// Variable represents a variable assignment in Ninja syntax.
//
// Variables are a fundamental building block in Ninja build files:
//   - Top-level variables: Defined at file scope, visible everywhere
//   - Rule-scoped variables: Defined inside a rule, visible only in that rule
//   - Build-scoped variables: Defined inside a build edge, visible only in that build
//
// Variables are referenced using $name or ${name} syntax.
// The curly brace form ${name} is used when the variable name is adjacent
// to other characters, e.g., ${name}ext or ${name}_suffix.
//
// Common variables available in rule commands:
//   - $in: Input file(s), space-separated
//   - $out: Output file(s), space-separated
//   - $in_dir: Directory of input file
//   - $out_dir: Directory of output file
//   - $name: Input file name without extension
//   - $name_ext: Input file name with extension
//   - $stem: Output file name without extension
//   - $ext: File extension
//
// Example Ninja syntax:
//
//	variable = value
//	rule compile
//	  command = gcc -c $in -o $out
//	  description = Compiling $in
type Variable struct {
	// Name is the variable name.
	// For top-level variables, this is the identifier used to reference it.
	// Variable names should be valid identifiers (alphanumeric and underscores).
	Name string

	// Value is the variable value.
	// This can be any string, including paths, commands, or compound values.
	// Values are stored as-is and written directly to the output.
	Value string
}

// Pool defines a named pool for limiting parallel task execution.
//
// Pools are a Ninja feature that allows you to restrict how many tasks
// can run in parallel. This is particularly useful for:
//   - Limiting memory-intensive operations (like linking large binaries)
//   - Controlling CPU usage for certain build phases
//   - Respecting license constraints (limited number of compiler licenses)
//   - Sequentializing tasks that cannot run concurrently
//
// Pool Format in .ninja file:
//
//	pool mypool
//	  depth = 4
//
// This creates a pool named "mypool" that allows at most 4 tasks to
// run in parallel. A build edge can then reference this pool:
//
//	build output.o: compile input.c
//	  pool = mypool
//
// If no pool is specified for a build edge, it uses the default pool
// which has unlimited depth (all available parallelism).
type Pool struct {
	// Name is the pool name, used to reference it in build edges.
	// Pool names should be unique within a build file.
	// They are referenced in Build.Pool field.
	Name string

	// Depth is the maximum number of parallel tasks allowed in this pool.
	// A depth of 1 means tasks run sequentially (no parallelism).
	// A depth of 0 is invalid.
	// The default pool has unlimited depth (effectively infinity).
	Depth int
}

// Rule defines a named build rule containing a command and optional properties.
//
// Rules are one of the core concepts in Ninja. A rule defines how to transform
// input files into output files. It contains the command to execute and
// various metadata about the build process.
//
// Rule attributes:
//   - Command (required): The command to execute. Can use $in, $out, and other
//     built-in variables. The command is run from the build directory.
//   - Description (optional): A short description shown during builds.
//     This is displayed instead of the full command for cleaner output.
//   - Depfile (optional): Path to a depfile (make-style dependency file).
//     Used to record implicit dependencies discovered during the build.
//     The depfile is read after the command runs and its contents are added
//     as implicit dependencies.
//   - Deps (optional): Specifies automatic dependency discovery style.
//     "gcc" enables GCC-style dependency generation (-MD -MP).
//     "msvc" enables MSVC-style dependency generation.
//     This tells Ninja how to parse dependency information from the command.
//   - Generator (optional): If true, this rule is a "generator".
//     Generator outputs are not used as implicit inputs to other rules.
//     This is useful for rules that generate other build files.
//   - Restat (optional): If true, Ninja will re-stat output files after
//     the command completes. If outputs haven't changed, Ninja will update
//     their timestamps as if they hadn't been modified. This is useful for
//     commands that don't actually change their outputs (e.g., touch commands).
//   - Variables (optional): Rule-scoped variables that are only visible
//     within this rule's command and other properties.
//
// Ninja syntax:
//
//	rule compile
//	  command = gcc -c $in -o $out
//	  description = Compiling $in
//	  deps = gcc
//	  restat = true
//
//	build main.o: compile main.c
type Rule struct {
	// Name is the rule name, used to reference it in build edges.
	// Rule names should be unique within a build file.
	// They are referenced in Build.RuleName field.
	Name string

	// Command is the command to execute.
	// This is a required field that specifies the actual command to run.
	// The command is executed from the build directory.
	// It can use Ninja's variable substitution:
	//   - $in: Input file(s), space-separated
	//   - $out: Output file(s), space-separated
	//   - $in_newer: Input files newer than all outputs
	//   - $out_dir: Directory portion of first output
	//   - $out_name: Name portion of first output
	//   - $out_base: Output file without extension
	// Use ${var} when the variable is adjacent to other characters.
	Command string

	// Description is a short description shown during builds.
	// This is displayed to show what operation is in progress.
	// Use $in to include the input file name in the description.
	// Shown in build output like: "ninja: Entering directory '...'"
	//                             "[1/10] Compiling main.c"
	Description string

	// Depfile is the path to a dependency file.
	// A depfile contains additional implicit dependencies discovered
	// during the build (e.g., #include statements in C files).
	// After the command runs, Ninja reads this file and adds its
	// contents as implicit dependencies.
	// The format is make-style: "output: input1 input2"
	Depfile string

	// Deps specifies the dependency extraction method.
	// Supported values:
	//   - "gcc": Use GCC-style deps (-MD -MP flags).
	//            Ninja runs the command with these flags and parses
	//            the .d file produced by the compiler.
	//   - "msvc": Use MSVC-style deps.
	//            Ninja parses the output for /Fi flags or similar.
	// When set, Ninja automatically extracts dependencies from
	// the compiler output without needing a depfile.
	Deps string

	// Generator indicates this rule is a "generator".
	// Generator rules are typically used to generate other build files.
	// Outputs of generator rules are NOT automatically used as implicit
	// inputs to other rules. This prevents circular dependencies.
	// Use this for rules that generate build.ninja files or other
	// generated configuration files.
	Generator bool

	// Restat controls whether Ninja re-stat outputs after command runs.
	// If true, Ninja will check if outputs actually changed.
	// If they haven't changed, Ninja treats them as unchanged.
	// This is useful for commands that always touch their outputs
	// (like touch or echo) but might not actually modify content.
	// Reduces unnecessary rebuilds when outputs are unchanged.
	Restat bool

	// Variables are rule-scoped variables.
	// These variables are only visible within this rule's command
	// and other properties. They are useful for rule-specific
	// configuration like compiler flags.
	// Format in output:
	//   varname = value
	Variables []Variable
}

// Build defines a build edge: outputs are produced from inputs using a rule.
//
// Build edges are the fundamental building blocks of a Ninja build graph.
// Each build edge specifies:
//   - The rule to use for building
//   - The output file(s)
//   - The input file(s)
//   - Optional implicit dependencies
//   - Optional order-only dependencies
//   - Optional pool to use
//   - Optional build-scoped variables
//
// Dependency Types:
//   - Inputs (explicit): The primary input files. Changes to these files
//     trigger a rebuild. These are listed directly after the rule name.
//   - Implicits (|): Implicit dependencies. Changes to these files also
//     trigger a rebuild, but they're not directly used in the command.
//     Use for header files, generated files, or other dependencies that
//     affect the build but aren't command arguments.
//   - OrderOnly (||): Order-only dependencies. These only affect the
//     build order - changes don't trigger rebuilds. Use for directories
//     that must exist before building, or for ensuring build order.
//
// Ninja syntax:
//
//	build output.o: compile input.c
//	build output.o: compile input.c | config.h
//	build output.o: compile input.c | config.h || stamp
//	build output.o: compile input.c || stamp
//
// Variables in builds:
//   - $in: All explicit inputs (space-separated)
//   - $out: All outputs (space-separated)
//   - $in_newer: Inputs newer than all outputs
//   - Pool: Limits parallel execution for this build edge
//   - Custom variables: Build-scoped, only visible in this edge
type Build struct {
	// RuleName references the rule to use for this build.
	// This must match the Name of a previously defined Rule.
	// The rule provides the command and metadata for building.
	RuleName string

	// Outputs is the list of output files produced by this build.
	// At least one output is required.
	// All outputs must be built by this rule; there is no "partial" build.
	// Multiple outputs are used for multiple-file rules (e.g., yacc).
	// In commands, $out refers to all outputs space-separated.
	Outputs []string

	// Inputs is the list of explicit input files.
	// These are the primary dependencies - changes trigger rebuilds.
	// Listed directly after the rule name in the build statement.
	// In commands, $in refers to all inputs space-separated.
	// Example: build out: rule in1 in2
	Inputs []string

	// Implicits is the list of implicit dependencies.
	// These are additional files that, when changed, trigger rebuilds
	// but aren't directly used in the command. They use "|" syntax.
	//
	// Use cases:
	//   - Header files for C/C++ compilation
	//   - Generated configuration files
	//   - External dependencies
	//   - Files read by the command but not as direct arguments
	//
	// Example:
	//   build main.o: compile main.c | config.h common.h
	//   This adds config.h and common.h as implicit dependencies.
	//   Rebuilds trigger if these files change.
	Implicits []string

	// OrderOnly is the list of order-only dependencies.
	// These only affect build order, not whether a rebuild occurs.
	// They use "||" syntax in the build statement.
	//
	// Use cases:
	//   - Ensuring directories exist before building
	//   - Ensuring generated files are up-to-date
	//   - Creating stamp files to enforce build order
	//   - Dependencies that should trigger builds but not rebuilds
	//     when changed (only initial creation matters)
	//
	// Example:
	//   build app: link main.o utils.o || headers.stamp
	//   headers.stamp must exist before linking, but changes to it
	//   don't trigger a re-link.
	OrderOnly []string

	// Variables are build-scoped variables.
	// These variables are only visible within this build edge's
	// properties. They override top-level variables for this build.
	// Useful for build-specific configuration.
	Variables []Variable

	// Pool is the name of the pool to use for this build.
	// If empty, the default pool (unlimited parallelism) is used.
	// If set to a pool name, that pool's depth limit applies.
	// Example: pool = compile_pool
	Pool string
}

// AddComment adds a comment line to the Ninja build file.
//
// Comments are useful for documenting the build file, explaining
// complex configurations, or adding metadata.
//
// The comment will have "# " prepended in the output.
// An empty string produces a blank line.
//
// Example:
//
//	nf.AddComment("Generated build file")
//	nf.AddComment("")  // blank line
func (nf *NinjaFile) AddComment(s string) {
	nf.Comments = append(nf.Comments, s)
}

// AddVariable adds a top-level variable definition.
//
// Top-level variables are visible throughout the entire build file.
// They can be referenced in rules, builds, and other variables using
// $name or ${name} syntax.
//
// Variables are written in the order they are added, before any
// rules or builds.
//
// Common uses:
//   - Directory paths (srcdir, builddir)
//   - Tool paths (gcc, clang)
//   - Common flags and options
//   - Configuration values
//
// Example:
//
//	nf.AddVariable("srcdir", ".")
//	nf.AddVariable("builddir", "build")
//	nf.AddVariable("cc", "gcc")
func (nf *NinjaFile) AddVariable(name, value string) {
	nf.Variables = append(nf.Variables, Variable{Name: name, Value: value})
}

// AddPool adds a pool definition for limiting parallel execution.
//
// Pools control how many tasks can run simultaneously.
// The depth parameter specifies the maximum parallelism.
// A pool must be defined before it can be used in builds.
//
// Common use cases:
//   - Compile pool: Limit parallel compilations (memory/CPU)
//   - Link pool: Limit parallel links (memory intensive)
//   - Test pool: Limit parallel tests (license/resource limits)
//
// Example:
//
//	nf.AddPool("compile", 4)  // Max 4 parallel compilations
//	nf.AddPool("link", 2)     // Max 2 parallel links
//
// Usage in build:
//
//	build main.o: compile main.c
//	  pool = compile
func (nf *NinjaFile) AddPool(name string, depth int) {
	nf.Pools = append(nf.Pools, Pool{Name: name, Depth: depth})
}

// AddRule adds a rule definition.
//
// Rules define how to build specific types of files.
// They must be defined before they can be used in builds.
//
// Rule fields:
//   - Name: Unique identifier for the rule
//   - Command: The command to execute (required)
//   - Description: Shown during builds (optional)
//   - Depfile: Path to dependency file (optional)
//   - Deps: Dependency style "gcc" or "msvc" (optional)
//   - Generator: Is this a generator rule (optional)
//   - Restat: Re-stat outputs after command (optional)
//   - Variables: Rule-scoped variables (optional)
//
// Example:
//
//	nf.AddRule(ninja_writer.Rule{
//	    Name:        "compile",
//	    Command:     "gcc -c $in -o $out",
//	    Description: "Compiling $in",
//	    Deps:        "gcc",
//	    Variables: []ninja_writer.Variable{
//	        {"CFLAGS", "-Wall -O2"},
//	    },
//	})
func (nf *NinjaFile) AddRule(r Rule) {
	nf.Rules = append(nf.Rules, r)
}

// AddBuild adds a build edge to the build graph.
//
// Build edges are the core of the Ninja build system.
// They specify how outputs are built from inputs using a rule.
//
// Build edge components:
//   - RuleName: Which rule to use for building
//   - Outputs: Files produced by this build
//   - Inputs: Explicit dependencies (changes trigger rebuilds)
//   - Implicits: Implicit dependencies (changes trigger rebuilds)
//   - OrderOnly: Order-only dependencies (only affect build order)
//   - Pool: Optional pool to limit parallelism
//   - Variables: Build-scoped variable overrides
//
// Example: Basic compilation
//
//	nf.AddBuild(ninja_writer.Build{
//	    RuleName: "compile",
//	    Inputs:   []string{"main.c", "utils.c"},
//	    Outputs:  []string{"main.o", "utils.o"},
//	})
//
// Example: With implicit and order-only dependencies
//
//	nf.AddBuild(ninja_writer.Build{
//	    RuleName:  "compile",
//	    Inputs:    []string{"main.c"},
//	    Outputs:   []string{"main.o"},
//	    Implicits: []string{"config.h", "common.h"},
//	    Pool:      "compile",
//	})
//
// Example: With order-only dependencies
//
//	nf.AddBuild(ninja_writer.Build{
//	    RuleName:  "link",
//	    Inputs:    []string{"main.o", "utils.o"},
//	    Outputs:   []string{"app"},
//	    OrderOnly: []string{"headers.stamp"},
//	})
func (nf *NinjaFile) AddBuild(b Build) {
	nf.Builds = append(nf.Builds, b)
}

// AddDefault adds a default target.
//
// Default targets are built when running "ninja" without specifying
// a target. Multiple calls add multiple default targets.
//
// When you run "ninja" (no arguments), it builds all default targets.
// This is useful for specifying the primary build artifact.
//
// Example:
//
//	nf.AddDefault("app")      // Build app by default
//	nf.AddDefault("tests")    // Also build tests
//
// Running "ninja" then builds both "app" and "tests".
func (nf *NinjaFile) AddDefault(target string) {
	nf.Defaults = append(nf.Defaults, target)
}

// WriteTo serializes the NinjaFile to Ninja syntax and writes to w.
//
// This method converts the structured NinjaFile into valid .ninja format.
// The serialization process follows the Ninja file format specification,
// outputting elements in a specific order with proper formatting.
//
// Output Order:
//  1. Comments (if any) - each with "# " prefix
//  2. Blank line (if comments and more content)
//  3. Variables - "name = value" format
//  4. Blank line (if variables and more content)
//  5. Pools - "pool name\n  depth = N" format
//  6. Blank line (if pools and more content)
//  7. Rules - "rule name\n  key = value" format
//  8. Build edges - "build outputs: rule inputs | implicits || orderonly" format
//  9. Default targets - "default target1 target2" format
//
// Serialization Details:
//
// Comments:
//   - Empty strings become blank lines
//   - Non-empty strings get "# " prefix
//   - A blank line separates comments from next section
//
// Variables:
//   - Format: "name = value"
//   - Two spaces around "=" for consistency with Ninja style
//   - Blank line separates from next section
//
// Pools:
//   - Format: "pool name" on one line, "  depth = N" indented on next
//   - Two-space indent for properties
//   - Blank line separates pools
//
// Rules:
//   - Format: "rule name" followed by indented "key = value" pairs
//   - Only non-empty optional fields are written
//   - Boolean fields (Generator, Restat) written as "true"
//   - Rule-scoped variables written as additional indented properties
//   - Blank line separates rules
//
// Builds:
//   - Format: "build output1 output2: rule input1 input2 | implicit1 || order1"
//   - Implicit deps (|) only written if present
//   - Order-only deps (||) only written if present
//   - Build-scoped variables written as indented properties
//   - No blank line between builds (compact output)
//
// Defaults:
//   - Format: "default target1 target2"
//   - Preceded by blank line
//   - Only written if defaults exist
//
// Returns:
//   - Number of bytes written
//   - Any error that occurred during writing
func (nf *NinjaFile) WriteTo(w io.Writer) (int64, error) {
	// strings.Builder is an efficient way to build the output string.
	// It avoids multiple allocations by growing a buffer as needed.
	var b strings.Builder

	// Write comments section.
	// Comments are written first with "# " prefix.
	// Empty strings become blank lines.
	for _, c := range nf.Comments {
		if c == "" {
			// Empty comment = blank line in output
			b.WriteString("\n")
		} else {
			// Non-empty: prepend "# " for comment syntax
			b.WriteString("# ")
			b.WriteString(c)
			b.WriteString("\n")
		}
	}
	// Add blank line after comments if there's more content
	if len(nf.Comments) > 0 {
		b.WriteString("\n")
	}

	// Write top-level variables.
	// Format: "name = value"
	// Variables are global and can be referenced with $name
	for _, v := range nf.Variables {
		b.WriteString(v.Name)
		b.WriteString(" = ")
		b.WriteString(v.Value)
		b.WriteString("\n")
	}
	// Add blank line after variables if there's more content
	if len(nf.Variables) > 0 {
		b.WriteString("\n")
	}

	// Write pool definitions.
	// Format:
	//   pool poolname
	//     depth = N
	// Pools limit parallel task execution.
	for _, p := range nf.Pools {
		b.WriteString("pool ")
		b.WriteString(p.Name)
		b.WriteString("\n")
		b.WriteString("  depth = ")
		b.WriteString(fmt.Sprintf("%d", p.Depth))
		b.WriteString("\n")
	}
	// Add blank line after pools if there's more content
	if len(nf.Pools) > 0 {
		b.WriteString("\n")
	}

	// Write rule definitions.
	// Format:
	//   rule rulename
	//     command = actual command
	//     description = shown during build (optional)
	//     depfile = path (optional)
	//     deps = gcc|msvc (optional)
	//     generator = true (optional)
	//     restat = true (optional)
	//     varname = value (optional, rule-scoped)
	for _, r := range nf.Rules {
		b.WriteString("rule ")
		b.WriteString(r.Name)
		b.WriteString("\n")
		// Command is required, always written
		writeIndentedVar(&b, "command", r.Command)
		// Description is optional
		if r.Description != "" {
			writeIndentedVar(&b, "description", r.Description)
		}
		// Depfile is optional
		if r.Depfile != "" {
			writeIndentedVar(&b, "depfile", r.Depfile)
		}
		// Deps is optional
		if r.Deps != "" {
			writeIndentedVar(&b, "deps", r.Deps)
		}
		// Generator boolean - only write if true
		if r.Generator {
			writeIndentedVar(&b, "generator", "true")
		}
		// Restat boolean - only write if true
		if r.Restat {
			writeIndentedVar(&b, "restat", "true")
		}
		// Write rule-scoped variables
		for _, v := range r.Variables {
			writeIndentedVar(&b, v.Name, v.Value)
		}
		b.WriteString("\n")
	}

	// Write build edges.
	// Format:
	//   build output1 output2: rulename input1 input2 | implicit1 implicit2 || order1
	//     pool = poolname (optional)
	//     varname = value (optional, build-scoped)
	//
	// Dependency syntax:
	//   - No prefix: explicit inputs, changes trigger rebuilds
	//   - | prefix: implicit deps, changes trigger rebuilds
	//   - || prefix: order-only deps, only affect build order
	for _, bld := range nf.Builds {
		b.WriteString("build ")
		// Outputs are space-separated
		b.WriteString(strings.Join(bld.Outputs, " "))
		b.WriteString(": ")
		// Rule name that defines how to build
		b.WriteString(bld.RuleName)
		// Write explicit inputs if present
		if len(bld.Inputs) > 0 {
			b.WriteString(" ")
			b.WriteString(strings.Join(bld.Inputs, " "))
		}
		// Write implicit dependencies (| syntax)
		// These are additional files that trigger rebuilds when changed
		if len(bld.Implicits) > 0 {
			b.WriteString(" | ")
			b.WriteString(strings.Join(bld.Implicits, " "))
		}
		// Write order-only dependencies (|| syntax)
		// These only affect build order, not rebuild decisions
		if len(bld.OrderOnly) > 0 {
			b.WriteString(" || ")
			b.WriteString(strings.Join(bld.OrderOnly, " "))
		}
		b.WriteString("\n")
		// Write pool assignment if specified
		if bld.Pool != "" {
			writeIndentedVar(&b, "pool", bld.Pool)
		}
		// Write build-scoped variables
		for _, v := range bld.Variables {
			writeIndentedVar(&b, v.Name, v.Value)
		}
	}

	// Write default targets.
	// Format: "default target1 target2 target3"
	// These are built when running "ninja" without arguments.
	if len(nf.Defaults) > 0 {
		b.WriteString("\ndefault ")
		b.WriteString(strings.Join(nf.Defaults, " "))
		b.WriteString("\n")
	}

	// Write the complete buffer to the writer.
	// io.WriteString returns the number of bytes written and any error.
	n, err := io.WriteString(w, b.String())
	return int64(n), err
}

// WriteFile writes the NinjaFile to the specified path.
//
// This method:
//  1. Creates any necessary parent directories
//  2. Creates (or truncates) the output file
//  3. Writes the serialized Ninja content
//
// Parameters:
//   - path: The file path to write to (can include directories)
//
// The directory portion of the path is created if it doesn't exist.
// If the file already exists, it is truncated and replaced.
//
// Example:
//
//	err := nf.WriteFile("build/build.ninja")
//	if err != nil {
//	    log.Fatal(err)
//	}
//
// This creates the "build" directory if needed and writes the
// Ninja file to "build/build.ninja".
func (nf *NinjaFile) WriteFile(path string) error {
	// Extract the directory portion of the path
	dir := filepath.Dir(path)
	// Create the directory and any necessary parents
	// Mode 0755: rwx-r-xr-x (owner full, others read/execute)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}
	// Create the file, truncating if it exists
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create file %s: %w", path, err)
	}
	// Close the file when done
	defer f.Close()
	// Write the Ninja content using WriteTo
	_, err = nf.WriteTo(f)
	if err != nil {
		return fmt.Errorf("failed to write ninja file %s: %w", path, err)
	}
	return nil
}

// writeIndentedVar is a helper function for writing indented key-value pairs.
//
// This is used for rule and build properties, which are indented by two spaces.
// The format is: "  name = value\n"
//
// Parameters:
//   - b: The string builder to write to
//   - name: The property name (key)
//   - value: The property value
func writeIndentedVar(b *strings.Builder, name, value string) {
	// Two-space indent for nested properties
	b.WriteString("  ")
	b.WriteString(name)
	b.WriteString(" = ")
	b.WriteString(value)
	b.WriteString("\n")
}
