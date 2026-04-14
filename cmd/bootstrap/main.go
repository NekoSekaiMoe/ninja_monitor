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
	"path/filepath"
	"runtime"
	"sync"
	"syscall"

	"ninja_monitor/internal/ninja_writer"
)

var (
	verbose       = flag.Bool("verbose", false, "Verbose output")
	bootstrap     = flag.Bool("bootstrap", false, "Run full bootstrap")
	clean         = flag.Bool("clean", false, "Clean build artifacts")
	jobs          = flag.Int("jobs", runtime.NumCPU(), "Parallel jobs")
	skipGo        = flag.Bool("skip-go", false, "Skip go build")
	skipSubmodule = flag.Bool("skip-submodule", false, "Skip submodule update")
	skipPatch     = flag.Bool("skip-patch", false, "Skip patch")
	skipStage1    = flag.Bool("skip-stage1", false, "Skip stage 1")
	skipStage2    = flag.Bool("skip-stage2", false, "Skip stage 2")
	skipMonitor   = flag.Bool("skip-monitor", false, "Skip monitor rebuild")
)

const (
	bootstrapEpoch = 1
)

var rootDir string

func main() {
	flag.Parse()

	exe, err := os.Executable()
	if err != nil {
		fatal("os.Executable: %v", err)
	}
	// binary lives at $ROOT/build/bootstrap, so root is parent of "build"
	rootDir = filepath.Dir(filepath.Dir(exe))
	os.Chdir(rootDir)

	if *clean {
		cleanBuild()
		return
	}

	checkBootstrapEpoch()

	if !*bootstrap {
		if err := generateNinjaFiles(rootDir); err != nil {
			fatal("generate ninja: %v", err)
		}
		fmt.Println("build.ninja generated (use --bootstrap to build)")
		return
	}

	if err := runFullBootstrap(); err != nil {
		fatal("%v", err)
	}
	fmt.Println("\nBootstrap completed successfully.")
}

func cleanBuild() {
	buildDir := filepath.Join(rootDir, "build")
	if err := os.RemoveAll(buildDir); err != nil {
		fatal("clean: %v", err)
	}
	fmt.Println("cleaned build/")

	submoduleDir := filepath.Join(rootDir, "dep", "ninja_mod")
	if _, err := os.Stat(submoduleDir); err == nil {
		if err := os.RemoveAll(submoduleDir); err != nil {
			fatal("clean submodule: %v", err)
		}
		fmt.Println("cleaned dep/ninja_mod/")
	}
}

var epochLock sync.Mutex

func checkBootstrapEpoch() {
	epochLock.Lock()
	defer epochLock.Unlock()

	epochFile := filepath.Join(rootDir, "build", fmt.Sprintf(".bootstrap.epoch.%d", bootstrapEpoch))
	if _, err := os.Stat(epochFile); os.IsNotExist(err) {
		buildDir := filepath.Join(rootDir, "build")
		if _, err := os.Stat(buildDir); err == nil {
			fmt.Println("warning: bootstrap epoch changed, consider running with --clean")
		}
	}
}

func getOutputDir(name string) string {
	dir := filepath.Join(rootDir, "build", name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		fatal("mkdir %s: %v", dir, err)
	}
	return dir
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func runFullBootstrap() error {
	if !*skipSubmodule {
		if err := runSubmoduleUpdate(); err != nil {
			return fmt.Errorf("submodule update: %v", err)
		}
	}

	if !*skipPatch {
		if err := runPatch(); err != nil {
			return fmt.Errorf("patch: %v", err)
		}
	}

	if err := os.MkdirAll(filepath.Join(rootDir, "build"), 0755); err != nil {
		return fmt.Errorf("mkdir build: %v", err)
	}

	if !*skipGo {
		if err := generateProto(); err != nil {
			return fmt.Errorf("proto generation: %v", err)
		}
		if err := goBuildMonitor(); err != nil {
			return fmt.Errorf("go build: %v", err)
		}
	}

	if err := generateStage1Ninja(); err != nil {
		return fmt.Errorf("generate stage1 ninja: %v", err)
	}

	if !*skipStage1 {
		if err := runStage1(); err != nil {
			return fmt.Errorf("stage1: %v", err)
		}
	}

	if err := generateStage2Ninja(); err != nil {
		return fmt.Errorf("generate stage2 ninja: %v", err)
	}

	if !*skipStage2 {
		if err := runStage2(); err != nil {
			return fmt.Errorf("stage2: %v", err)
		}
	}

	if err := generateStage3Ninja(); err != nil {
		return fmt.Errorf("generate stage3 ninja: %v", err)
	}

	if err := runStage3(); err != nil {
		return fmt.Errorf("stage3: %v", err)
	}

	writeEpochFile()
	return nil
}

func writeEpochFile() {
	epochLock.Lock()
	defer epochLock.Unlock()
	buildDir := filepath.Join(rootDir, "build")
	os.MkdirAll(buildDir, 0755)
	epochFile := filepath.Join(buildDir, fmt.Sprintf(".bootstrap.epoch.%d", bootstrapEpoch))
	os.WriteFile(epochFile, []byte(fmt.Sprintf("%d\n", bootstrapEpoch)), 0644)
}

func runSubmoduleUpdate() error {
	ninjaModDir := filepath.Join(rootDir, "dep", "ninja_mod")

	if fileExists(filepath.Join(ninjaModDir, ".git")) {
		if *verbose {
			fmt.Println("  submodule already initialized")
		}
		return nil
	}

	cmd := exec.Command("git", "submodule", "update", "--init", "dep/ninja_mod")
	cmd.Dir = rootDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runPatch() error {
	patchFile := filepath.Join(rootDir, "dep", "i.patch")
	if !fileExists(patchFile) {
		fmt.Println("  patch not found, skipping")
		return nil
	}

	ninjaModDir := filepath.Join(rootDir, "dep", "ninja_mod")

	checkCmd := exec.Command("git", "apply", "--reverse", "--check", patchFile)
	checkCmd.Dir = ninjaModDir
	if err := checkCmd.Run(); err == nil {
		if *verbose {
			fmt.Println("  patch already applied")
		}
		return nil
	}

	cmd := exec.Command("git", "apply", patchFile)
	cmd.Dir = ninjaModDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func checkTool(name string) error {
	path, err := exec.LookPath(name)
	if err != nil {
		return fmt.Errorf("required tool not found: %s", name)
	}
	if *verbose {
		fmt.Printf("  found %s: %s\n", name, path)
	}
	return nil
}

func generateProto() error {
	if err := checkTool("protoc"); err != nil {
		return err
	}

	if err := checkTool("protoc-gen-go"); err != nil {
		return err
	}

	if err := exec.Command("go", "mod", "download").Run(); err != nil {
		return fmt.Errorf("go mod download: %v", err)
	}

	protoDir := filepath.Join(rootDir, "internal", "ninja_frontend")

	cmd := exec.Command("protoc",
		"--go_out=.",
		"--go_opt=paths=source_relative",
		"frontend.proto",
	)
	cmd.Dir = protoDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func goBuildMonitor() error {
	outBin := filepath.Join(rootDir, "build", "ninja_monitor_gobuild")
	os.MkdirAll(filepath.Join(rootDir, "build"), 0755)

	cmd := exec.Command("go", "build", "-o", outBin, "./cmd/ninja_monitor")
	cmd.Dir = rootDir
	if *verbose {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	return cmd.Run()
}

func ninjaSources() []string {
	return []string{
		"build", "build_log", "clean", "clparser", "debug_flags",
		"depfile_parser", "deps_log", "disk_interface", "dyndep", "dyndep_parser",
		"edit_distance", "eval_env", "graph", "graphviz", "lexer",
		"line_printer", "manifest_parser", "metrics", "parser", "proto",
		"state", "status", "string_piece_util", "subprocess-posix", "util",
		"version", "ninja",
	}
}

var cflags = "-std=c++17 -O3 -Wno-unused-parameter -DNDEBUG -Wall -Wextra -Wno-deprecated -fno-rtti -pthread"

func addCommonRules(nf *ninja_writer.NinjaFile, buildDir string) {
	nf.AddRule(ninja_writer.Rule{
		Name:        "cxx",
		Command:     "$cxx -MMD -MT $out -MF $out.d $cflags -c $in -o $out",
		Description: "CXX $out",
		Depfile:     "$out.d",
		Deps:        "gcc",
	})

	nf.AddRule(ninja_writer.Rule{
		Name:        "ar",
		Command:     "rm -f $out && ar crs $out $in",
		Description: "AR $out",
	})

	nf.AddRule(ninja_writer.Rule{
		Name:        "link",
		Command:     "$cxx $ldflags -o $out $in -L$builddir -lninja -lpthread",
		Description: "LINK $out",
	})
}

func addCommonVariables(nf *ninja_writer.NinjaFile, buildDir, srcDir string) {
	nf.AddVariable("ninja_required_version", "1.3")
	nf.AddVariable("builddir", buildDir)
	nf.AddVariable("ninja_src", srcDir)
	nf.AddVariable("cxx", "g++")
	nf.AddVariable("cflags", cflags)
	nf.AddVariable("ar", "ar")
	nf.AddVariable("ldflags", "-pthread")
}

func addCommonBuilds(nf *ninja_writer.NinjaFile, buildDir, srcVar string) {
	var objs []string
	for _, name := range ninjaSources() {
		obj := filepath.Join("$builddir", name+".o")
		objs = append(objs, obj)
		nf.AddBuild(ninja_writer.Build{
			RuleName: "cxx",
			Outputs:  []string{obj},
			Inputs:   []string{filepath.Join(srcVar, name+".cc")},
		})
	}

	nf.AddBuild(ninja_writer.Build{
		RuleName: "ar",
		Outputs:  []string{filepath.Join("$builddir", "libninja.a")},
		Inputs:   objs,
	})

	nf.AddBuild(ninja_writer.Build{
		RuleName:  "link",
		Outputs:   []string{filepath.Join("$builddir", "ninja_mod")},
		Inputs:    []string{filepath.Join("$builddir", "ninja.o")},
		Implicits: []string{filepath.Join("$builddir", "libninja.a")},
	})
}

func generateStage1Ninja() error {
	buildDir := getOutputDir("ninja_stage1")
	srcDir := filepath.Join(rootDir, "dep", "ninja_mod", "src")

	nf := &ninja_writer.NinjaFile{}
	nf.AddComment("Stage 1: build ninja_mod from source (no monitoring)")
	nf.AddVariable("root", srcDir)
	addCommonVariables(nf, buildDir, srcDir)
	addCommonRules(nf, buildDir)
	addCommonBuilds(nf, buildDir, "$root")

	nf.AddDefault(filepath.Join("$builddir", "ninja_mod"))

	return nf.WriteFile(filepath.Join(rootDir, "build", "ninja_build.ninja"))
}

func runStage1() error {
	cmd := exec.Command("ninja", "-f", filepath.Join(rootDir, "build", "ninja_build.ninja"), "-j", fmt.Sprintf("%d", *jobs))
	cmd.Dir = rootDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func generateStage2Ninja() error {
	buildDir := getOutputDir("ninja_stage2")
	srcDir := filepath.Join(rootDir, "dep", "ninja_mod", "src")
	stage1Bin := filepath.Join(rootDir, "build", "ninja_stage1", "ninja_mod")

	nf := &ninja_writer.NinjaFile{}
	nf.AddComment("Stage 2: rebuild ninja_mod + ninja_monitor")
	nf.AddVariable("ninja", stage1Bin)
	nf.AddVariable("go_root", rootDir)
	addCommonVariables(nf, buildDir, srcDir)
	addCommonRules(nf, buildDir)
	nf.AddRule(ninja_writer.Rule{
		Name:        "go_build",
		Command:     "cd $go_root && go build -o $out ./cmd/ninja_monitor",
		Description: "GO BUILD $out",
	})
	addCommonBuilds(nf, buildDir, "$ninja_src")

	nf.AddBuild(ninja_writer.Build{
		RuleName:  "go_build",
		Outputs:   []string{filepath.Join(rootDir, "build", "ninja_monitor")},
		Inputs:    []string{filepath.Join("$go_root", "cmd", "ninja_monitor", "main.go")},
		Implicits: []string{filepath.Join("$go_root", "go.mod"), filepath.Join("$go_root", "go.sum")},
	})

	nf.AddDefault(filepath.Join("$builddir", "ninja_mod"))
	nf.AddDefault(filepath.Join(rootDir, "build", "ninja_monitor"))

	return nf.WriteFile(filepath.Join(rootDir, "build", "ninja_build_stage2.ninja"))
}

func runStage2() error {
	stage1Ninja := filepath.Join(rootDir, "build", "ninja_stage1", "ninja_mod")
	monitorBin := filepath.Join(rootDir, "build", "ninja_monitor_gobuild")

	if !fileExists(stage1Ninja) {
		return fmt.Errorf("stage1 ninja not found: %s", stage1Ninja)
	}

	fifo := filepath.Join(os.TempDir(), ".ninja_fifo")
	os.Remove(fifo)
	if err := syscall.Mkfifo(fifo, 0644); err != nil {
		return fmt.Errorf("mkfifo: %v", err)
	}
	defer os.Remove(fifo)

	cmd := exec.Command(monitorBin, "--ninja", stage1Ninja, "--", "-f", filepath.Join(rootDir, "build", "ninja_build_stage2.ninja"), "-j", fmt.Sprintf("%d", *jobs))
	cmd.Dir = rootDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "NINJA_STATUS=stage2: ")

	return cmd.Run()
}

func generateStage3Ninja() error {
	buildDir := getOutputDir("ninja_stage3")
	srcDir := filepath.Join(rootDir, "dep", "ninja_mod", "src")
	stage2Bin := filepath.Join(rootDir, "build", "ninja_stage2", "ninja_mod")

	nf := &ninja_writer.NinjaFile{}
	nf.AddComment("Stage 3: final build")
	nf.AddVariable("ninja", stage2Bin)
	nf.AddVariable("go_root", rootDir)
	addCommonVariables(nf, buildDir, srcDir)
	addCommonRules(nf, buildDir)
	nf.AddRule(ninja_writer.Rule{
		Name:        "go_build",
		Command:     "cd $go_root && go build -o $out ./cmd/ninja_monitor",
		Description: "GO BUILD $out",
	})
	addCommonBuilds(nf, buildDir, "$ninja_src")

	nf.AddBuild(ninja_writer.Build{
		RuleName:  "go_build",
		Outputs:   []string{filepath.Join(rootDir, "build", "ninja_monitor")},
		Inputs:    []string{filepath.Join("$go_root", "cmd", "ninja_monitor", "main.go")},
		Implicits: []string{filepath.Join("$go_root", "go.mod"), filepath.Join("$go_root", "go.sum")},
	})

	outBin := filepath.Join(rootDir, "build", "out", "bin")
	nf.AddRule(ninja_writer.Rule{
		Name:        "copy",
		Command:     "mkdir -p " + outBin + " && cp -f $in $out",
		Description: "COPY $out",
	})

	nf.AddBuild(ninja_writer.Build{
		RuleName: "copy",
		Outputs:  []string{filepath.Join(outBin, "ninja_mod")},
		Inputs:   []string{filepath.Join("$builddir", "ninja_mod")},
	})

	nf.AddBuild(ninja_writer.Build{
		RuleName: "copy",
		Outputs:  []string{filepath.Join(outBin, "ninja_monitor")},
		Inputs:   []string{filepath.Join(rootDir, "build", "ninja_monitor")},
	})

	nf.AddDefault(filepath.Join(outBin, "ninja_mod"))
	nf.AddDefault(filepath.Join(outBin, "ninja_monitor"))

	return nf.WriteFile(filepath.Join(rootDir, "build", "ninja_build_stage3.ninja"))
}

func runStage3() error {
	stage2Ninja := filepath.Join(rootDir, "build", "ninja_stage2", "ninja_mod")
	monitorBin := filepath.Join(rootDir, "build", "ninja_monitor")

	if !fileExists(stage2Ninja) {
		return fmt.Errorf("stage2 ninja not found: %s", stage2Ninja)
	}

	fifo := filepath.Join(os.TempDir(), ".ninja_fifo")
	os.Remove(fifo)
	if err := syscall.Mkfifo(fifo, 0644); err != nil {
		return fmt.Errorf("mkfifo: %v", err)
	}
	defer os.Remove(fifo)

	cmd := exec.Command(monitorBin, "--verbose", "--ninja", stage2Ninja, "--", "-f", filepath.Join(rootDir, "build", "ninja_build_stage3.ninja"), "-j", fmt.Sprintf("%d", *jobs))
	cmd.Dir = rootDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

func generateNinjaFiles(root string) error {
	if err := generateStage1Ninja(); err != nil {
		return err
	}
	if err := generateStage2Ninja(); err != nil {
		return err
	}
	return generateStage3Ninja()
}
