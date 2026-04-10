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
// Instead of string concatenation, callers build typed structures (Rule, Build, etc.)
// and serialize them to valid .ninja syntax.
package ninja_writer

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// NinjaFile represents a complete .ninja build file.
type NinjaFile struct {
	Comments  []string
	Variables []Variable
	Pools     []Pool
	Rules     []Rule
	Builds    []Build
	Defaults  []string
}

// Variable is a top-level or scoped variable assignment.
type Variable struct {
	Name  string
	Value string
}

// Pool defines a named pool with a depth limit.
type Pool struct {
	Name  string
	Depth int
}

// Rule defines a named build rule with a command and optional properties.
type Rule struct {
	Name        string
	Command     string // required
	Description string
	Depfile     string
	Deps        string // "gcc" or "msvc"
	Generator   bool
	Restat      bool
	Variables   []Variable // rule-scoped variables
}

// Build defines a build edge: outputs are produced from inputs by a rule.
type Build struct {
	RuleName  string
	Outputs   []string // required, at least one
	Inputs    []string // explicit inputs
	Implicits []string // implicit deps (after |)
	OrderOnly []string // order-only deps (after ||)
	Variables []Variable
	Pool      string
}

// AddComment appends a comment line.
func (nf *NinjaFile) AddComment(s string) {
	nf.Comments = append(nf.Comments, s)
}

// AddVariable appends a top-level variable.
func (nf *NinjaFile) AddVariable(name, value string) {
	nf.Variables = append(nf.Variables, Variable{Name: name, Value: value})
}

// AddPool appends a pool definition.
func (nf *NinjaFile) AddPool(name string, depth int) {
	nf.Pools = append(nf.Pools, Pool{Name: name, Depth: depth})
}

// AddRule appends a rule definition.
func (nf *NinjaFile) AddRule(r Rule) {
	nf.Rules = append(nf.Rules, r)
}

// AddBuild appends a build edge.
func (nf *NinjaFile) AddBuild(b Build) {
	nf.Builds = append(nf.Builds, b)
}

// AddDefault appends a default target.
func (nf *NinjaFile) AddDefault(target string) {
	nf.Defaults = append(nf.Defaults, target)
}

// WriteTo serializes the NinjaFile to w.
func (nf *NinjaFile) WriteTo(w io.Writer) (int64, error) {
	var b strings.Builder

	for _, c := range nf.Comments {
		if c == "" {
			b.WriteString("\n")
		} else {
			b.WriteString("# ")
			b.WriteString(c)
			b.WriteString("\n")
		}
	}
	if len(nf.Comments) > 0 {
		b.WriteString("\n")
	}

	for _, v := range nf.Variables {
		b.WriteString(v.Name)
		b.WriteString(" = ")
		b.WriteString(v.Value)
		b.WriteString("\n")
	}
	if len(nf.Variables) > 0 {
		b.WriteString("\n")
	}

	for _, p := range nf.Pools {
		b.WriteString("pool ")
		b.WriteString(p.Name)
		b.WriteString("\n")
		b.WriteString("  depth = ")
		b.WriteString(fmt.Sprintf("%d", p.Depth))
		b.WriteString("\n")
	}
	if len(nf.Pools) > 0 {
		b.WriteString("\n")
	}

	for _, r := range nf.Rules {
		b.WriteString("rule ")
		b.WriteString(r.Name)
		b.WriteString("\n")
		writeIndentedVar(&b, "command", r.Command)
		if r.Description != "" {
			writeIndentedVar(&b, "description", r.Description)
		}
		if r.Depfile != "" {
			writeIndentedVar(&b, "depfile", r.Depfile)
		}
		if r.Deps != "" {
			writeIndentedVar(&b, "deps", r.Deps)
		}
		if r.Generator {
			writeIndentedVar(&b, "generator", "true")
		}
		if r.Restat {
			writeIndentedVar(&b, "restat", "true")
		}
		for _, v := range r.Variables {
			writeIndentedVar(&b, v.Name, v.Value)
		}
		b.WriteString("\n")
	}

	for _, bld := range nf.Builds {
		b.WriteString("build ")
		b.WriteString(strings.Join(bld.Outputs, " "))
		b.WriteString(": ")
		b.WriteString(bld.RuleName)
		if len(bld.Inputs) > 0 {
			b.WriteString(" ")
			b.WriteString(strings.Join(bld.Inputs, " "))
		}
		if len(bld.Implicits) > 0 {
			b.WriteString(" | ")
			b.WriteString(strings.Join(bld.Implicits, " "))
		}
		if len(bld.OrderOnly) > 0 {
			b.WriteString(" || ")
			b.WriteString(strings.Join(bld.OrderOnly, " "))
		}
		b.WriteString("\n")
		if bld.Pool != "" {
			writeIndentedVar(&b, "pool", bld.Pool)
		}
		for _, v := range bld.Variables {
			writeIndentedVar(&b, v.Name, v.Value)
		}
	}

	if len(nf.Defaults) > 0 {
		b.WriteString("\ndefault ")
		b.WriteString(strings.Join(nf.Defaults, " "))
		b.WriteString("\n")
	}

	n, err := io.WriteString(w, b.String())
	return int64(n), err
}

// WriteFile creates the directory if needed and writes the NinjaFile to path.
func (nf *NinjaFile) WriteFile(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create file %s: %w", path, err)
	}
	defer f.Close()
	_, err = nf.WriteTo(f)
	if err != nil {
		return fmt.Errorf("failed to write ninja file %s: %w", path, err)
	}
	return nil
}

func writeIndentedVar(b *strings.Builder, name, value string) {
	b.WriteString("  ")
	b.WriteString(name)
	b.WriteString(" = ")
	b.WriteString(value)
	b.WriteString("\n")
}
