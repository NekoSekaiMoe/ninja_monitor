#!/usr/bin/env python3
"""
Ninja Monitor Bootstrap Script

This script implements a bootstrap process similar to Soong's bootstrap flow:
  1. go build              - Build ninja_monitor_gobuild (go build)
  2. submodule update      - Initialize ninja submodule
  3. patch ninja           - Apply CMake support patch
  4. ninja_stage1          - Build ninja with CMake (first stage)
  5. ninja_stage2          - Rebuild ninja with ninja_stage1 (self-hosting)
  6. ninja_monitor         - Rebuild ninja_monitor with ninja_stage2
  7. generate build.ninja  - Create build.ninja file
  8. ninja_stage3          - Execute ninja with ninja_monitor (only with --bootstrap)

Usage:
  python3 bootstrap.py              # Generate build.ninja only
  python3 bootstrap.py --bootstrap  # Full bootstrap including running ninja
"""

import argparse
import os
import subprocess
import sys
import shutil
import multiprocessing
from pathlib import Path
from typing import Optional, List

# Bootstrap epoch for invalidating cached builds
BOOTSTRAP_EPOCH = 1

# Directory paths
SCRIPT_DIR = Path(__file__).parent.resolve()
OUT_DIR = SCRIPT_DIR / "build"           # Build outputs
DEP_DIR = SCRIPT_DIR / "dep"
NINJA_MOD_DIR = DEP_DIR / "ninja_mod"
PATCH_FILE = DEP_DIR / "i.patch"

# Stage directories
NINJA_STAGE1_DIR = OUT_DIR / "ninja_stage1"  # CMake build output
NINJA_STAGE2_DIR = OUT_DIR / "ninja_stage2"  # Ninja rebuild output
NINJA_STAGE3_DIR = OUT_DIR / "ninja_stage3"  # Final build output

# Binary paths
NINJA_MONITOR_GOBUILD = OUT_DIR / "ninja_monitor_gobuild"  # Go build version
NINJA_MONITOR_BIN = OUT_DIR / "ninja_monitor"              # Ninja build version
NINJA_STAGE1_BIN = NINJA_STAGE1_DIR / "ninja_mod"          # CMake built ninja
NINJA_STAGE2_BIN = NINJA_STAGE2_DIR / "ninja_mod"          # Ninja rebuilt ninja
BUILD_NINJA = NINJA_STAGE3_DIR / "build.ninja"

EPOCH_FILE = OUT_DIR / f".bootstrap.epoch.{BOOTSTRAP_EPOCH}"


class Colors:
    """ANSI color codes for terminal output."""
    RED = '\033[91m'
    GREEN = '\033[92m'
    YELLOW = '\033[93m'
    BLUE = '\033[94m'
    RESET = '\033[0m'
    BOLD = '\033[1m'


class Logger:
    """Simple logger with colored output."""
    
    def __init__(self, verbose: bool = False):
        self.verbose = verbose
    
    def info(self, msg: str):
        print(f"{Colors.BLUE}[INFO]{Colors.RESET} {msg}")
    
    def success(self, msg: str):
        print(f"{Colors.GREEN}[OK]{Colors.RESET} {msg}")
    
    def warn(self, msg: str):
        print(f"{Colors.YELLOW}[WARN]{Colors.RESET} {msg}")
    
    def error(self, msg: str):
        print(f"{Colors.RED}[ERROR]{Colors.RESET} {msg}", file=sys.stderr)
    
    def step(self, step_num: int, total: int, msg: str):
        print(f"{Colors.BOLD}[{step_num}/{total}]{Colors.RESET} {msg}")
    
    def debug(self, msg: str):
        if self.verbose:
            print(f"  {msg}")


def run_command(cmd: List[str], cwd: Optional[Path] = None, 
                check: bool = True, capture: bool = False,
                logger: Optional[Logger] = None) -> subprocess.CompletedProcess:
    """Run a shell command with proper error handling."""
    if logger:
        logger.debug(f"Running: {' '.join(str(c) for c in cmd)}")
    
    try:
        result = subprocess.run(
            cmd,
            cwd=cwd,
            check=check,
            capture_output=capture,
            text=True
        )
        return result
    except subprocess.CalledProcessError as e:
        if logger:
            logger.error(f"Command failed: {' '.join(str(c) for c in cmd)}")
            if e.stdout:
                logger.error(f"stdout: {e.stdout}")
            if e.stderr:
                logger.error(f"stderr: {e.stderr}")
        raise


def step1_go_build(logger: Logger) -> bool:
    """
    Step 1: Build ninja_monitor_gobuild using go build.
    
    Output: out/ninja_monitor_gobuild
    """
    logger.step(1, 8, "Building ninja_monitor_gobuild (go build)")
    
    go_cmd = shutil.which("go")
    if not go_cmd:
        logger.error("Go compiler not found. Please install Go.")
        return False
    
    OUT_DIR.mkdir(parents=True, exist_ok=True)
    
    # Download dependencies first
    logger.debug("Downloading Go dependencies...")
    try:
        run_command([go_cmd, "mod", "download"], cwd=SCRIPT_DIR, logger=logger)
    except subprocess.CalledProcessError:
        logger.warn("go mod download failed, continuing anyway...")
    
    cmd = [go_cmd, "build", "-o", str(NINJA_MONITOR_GOBUILD), "./cmd/ninja_monitor"]
    
    try:
        run_command(cmd, cwd=SCRIPT_DIR, logger=logger)
        logger.success(f"Built {NINJA_MONITOR_GOBUILD.relative_to(SCRIPT_DIR)}")
        return True
    except subprocess.CalledProcessError:
        return False


def step2_submodule_update(logger: Logger) -> bool:
    """
    Step 2: Initialize and update ninja submodule.
    """
    logger.step(2, 8, "Updating ninja submodule")
    
    if not (SCRIPT_DIR / ".gitmodules").exists():
        logger.warn("No .gitmodules found, skipping submodule update")
        return True
    
    if NINJA_MOD_DIR.exists() and (NINJA_MOD_DIR / ".git").exists():
        logger.debug("Submodule already initialized")
        return True
    
    try:
        run_command(
            ["git", "submodule", "update", "--init", str(NINJA_MOD_DIR.relative_to(SCRIPT_DIR))],
            cwd=SCRIPT_DIR, logger=logger
        )
        logger.success("Submodule updated")
        return True
    except subprocess.CalledProcessError:
        try:
            run_command(["git", "submodule", "init"], cwd=SCRIPT_DIR, logger=logger)
            run_command(["git", "submodule", "update"], cwd=SCRIPT_DIR, logger=logger)
            logger.success("Submodule updated (via init/update)")
            return True
        except subprocess.CalledProcessError:
            return False


def step3_patch_ninja(logger: Logger) -> bool:
    """
    Step 3: Apply patch to ninja for CMake support.
    """
    logger.step(3, 8, "Patching ninja source")
    
    if not PATCH_FILE.exists():
        logger.warn(f"Patch file not found: {PATCH_FILE}")
        return True
    
    if not NINJA_MOD_DIR.exists():
        logger.error(f"Ninja source directory not found: {NINJA_MOD_DIR}")
        return False
    
    cmake_file = NINJA_MOD_DIR / "CMakeLists.txt"
    if cmake_file.exists():
        logger.debug("Patch already applied (CMakeLists.txt exists)")
        return True
    
    patch_rel_path = "../i.patch"
    
    try:
        run_command(
            ["git", "apply", "--check", patch_rel_path],
            cwd=NINJA_MOD_DIR, check=True, capture=True, logger=logger
        )
    except subprocess.CalledProcessError:
        if cmake_file.exists():
            logger.success("Patch already applied")
            return True
        logger.error("Cannot apply patch - conflicts detected")
        return False
    
    try:
        run_command(["git", "apply", patch_rel_path], cwd=NINJA_MOD_DIR, logger=logger)
        logger.success("Patch applied successfully")
        return True
    except subprocess.CalledProcessError:
        return False


def step4_ninja_stage1(logger: Logger) -> bool:
    """
    Step 4: Build ninja_stage1 using CMake.
    
    Output: out/ninja_stage1/ninja_mod
    """
    logger.step(4, 8, "Building ninja_stage1 (CMake)")
    
    cmake_cmd = shutil.which("cmake")
    if not cmake_cmd:
        logger.error("CMake not found. Please install CMake.")
        return False
    
    NINJA_STAGE1_DIR.mkdir(parents=True, exist_ok=True)
    
    # Configure
    logger.debug("Configuring with CMake...")
    try:
        run_command(
            [cmake_cmd, "-S", str(NINJA_MOD_DIR), "-B", str(NINJA_STAGE1_DIR)],
            cwd=SCRIPT_DIR, logger=logger
        )
    except subprocess.CalledProcessError:
        return False
    
    # Build with parallel jobs
    logger.debug("Building with CMake...")
    parallel_jobs = multiprocessing.cpu_count()
    try:
        run_command(
            [cmake_cmd, "--build", str(NINJA_STAGE1_DIR), "-j", str(parallel_jobs)],
            cwd=SCRIPT_DIR, logger=logger
        )
    except subprocess.CalledProcessError:
        return False
    
    if NINJA_STAGE1_BIN.exists():
        logger.success(f"Built {NINJA_STAGE1_BIN.relative_to(SCRIPT_DIR)}")
        return True
    else:
        logger.error(f"Ninja binary not found at {NINJA_STAGE1_BIN}")
        return False


def step5_ninja_stage2(logger: Logger) -> bool:
    """
    Step 5: Rebuild ninja with CMake using ninja_stage1 as backend.
    
    Uses CMake with ninja_stage1 as the ninja generator.
    Output: out/ninja_stage2/ninja_mod
    """
    logger.step(5, 8, "Building ninja_stage2 (cmake + ninja_stage1)")
    
    cmake_cmd = shutil.which("cmake")
    if not cmake_cmd:
        logger.error("CMake not found. Please install CMake.")
        return False
    
    if not NINJA_STAGE1_BIN.exists():
        logger.error(f"ninja_stage1 not found: {NINJA_STAGE1_BIN}")
        return False
    
    NINJA_STAGE2_DIR.mkdir(parents=True, exist_ok=True)
    
    # Configure with CMake, using ninja_stage1 as the ninja backend
    logger.debug("Configuring with CMake (ninja backend: ninja_stage1)...")
    try:
        run_command(
            [cmake_cmd, "-S", str(NINJA_MOD_DIR), "-B", str(NINJA_STAGE2_DIR),
             "-G", "Ninja",
             f"-DCMAKE_MAKE_PROGRAM={NINJA_STAGE1_BIN}"],
            cwd=SCRIPT_DIR, logger=logger
        )
    except subprocess.CalledProcessError:
        return False
    
    # Build with CMake (will use ninja_stage1 internally)
    logger.debug("Building with CMake...")
    parallel_jobs = multiprocessing.cpu_count()
    try:
        run_command(
            [cmake_cmd, "--build", str(NINJA_STAGE2_DIR), "-j", str(parallel_jobs)],
            cwd=SCRIPT_DIR, logger=logger
        )
    except subprocess.CalledProcessError:
        return False
    
    # Verify ninja was built (cmake names it ninja_mod)
    ninja_stage2_bin = NINJA_STAGE2_DIR / "ninja_mod"
    if ninja_stage2_bin.exists():
        # Copy to expected location
        if ninja_stage2_bin != NINJA_STAGE2_BIN:
            shutil.copy(ninja_stage2_bin, NINJA_STAGE2_BIN)
        logger.success(f"Built {NINJA_STAGE2_BIN.relative_to(SCRIPT_DIR)}")
        return True
    else:
        logger.error(f"Ninja binary not found at {ninja_stage2_bin}")
        return False


def step6_ninja_monitor(logger: Logger) -> bool:
    """
    Step 6: Rebuild ninja_monitor with ninja_stage2.
    
    Uses ninja_stage2 and ninja_monitor_gobuild to rebuild ninja_monitor.
    Output: out/ninja_monitor
    """
    logger.step(6, 8, "Building ninja_monitor (ninja self-hosting)")
    
    if not NINJA_STAGE2_BIN.exists():
        logger.error(f"ninja_stage2 not found: {NINJA_STAGE2_BIN}")
        return False
    
    if not NINJA_MONITOR_GOBUILD.exists():
        logger.error(f"ninja_monitor_gobuild not found: {NINJA_MONITOR_GOBUILD}")
        return False
    
    # Generate a simple build.ninja for ninja_monitor
    monitor_build_ninja = OUT_DIR / "ninja_monitor_build.ninja"
    parallel_jobs = multiprocessing.cpu_count()
    
    content = f'''# Build ninja_monitor
rule go_build
  command = go build -o $out ./cmd/ninja_monitor
  description = GO BUILD $out

build ninja_monitor: go_build
'''
    monitor_build_ninja.write_text(content)
    
    # Use ninja_stage2 to build ninja_monitor
    logger.debug("Building ninja_monitor with ninja_stage2...")
    try:
        run_command(
            [str(NINJA_STAGE2_BIN), "-f", str(monitor_build_ninja),
             "-j", str(parallel_jobs), "ninja_monitor"],
            cwd=SCRIPT_DIR, logger=logger
        )
    except subprocess.CalledProcessError:
        # Fallback: just copy ninja_monitor_gobuild
        logger.debug("Falling back to copy ninja_monitor_gobuild")
        shutil.copy(NINJA_MONITOR_GOBUILD, NINJA_MONITOR_BIN)
    
    # Verify output
    if not NINJA_MONITOR_BIN.exists():
        # Check if built in current dir
        built_bin = SCRIPT_DIR / "ninja_monitor"
        if built_bin.exists():
            shutil.move(built_bin, NINJA_MONITOR_BIN)
    
    if NINJA_MONITOR_BIN.exists():
        logger.success(f"Built {NINJA_MONITOR_BIN.relative_to(SCRIPT_DIR)}")
        return True
    else:
        logger.error(f"ninja_monitor not found at {NINJA_MONITOR_BIN}")
        return False


def step7_generate_build_ninja(logger: Logger) -> bool:
    """
    Step 7: Generate build.ninja for stage3.
    
    This generates rules to build:
    - ninja_stage3 (ninja_mod) using cmake + ninja_stage2
    - ninja_monitor using go build
    - Copy ninja_mod to build directory
    
    Output: out/ninja_stage3/build.ninja
    """
    logger.step(7, 8, "Generating build.ninja")
    
    NINJA_STAGE3_DIR.mkdir(parents=True, exist_ok=True)
    
    # Get relative paths
    ninja_mod_rel = NINJA_MOD_DIR.relative_to(SCRIPT_DIR)
    stage2_rel = NINJA_STAGE2_DIR.relative_to(SCRIPT_DIR)
    stage3_rel = NINJA_STAGE3_DIR.relative_to(SCRIPT_DIR)
    out_rel = OUT_DIR.relative_to(SCRIPT_DIR)
    build_rel = Path("build")
    
    # Use absolute path for CMAKE_MAKE_PROGRAM (cmake requires absolute path)
    ninja_stage2_bin_abs = NINJA_STAGE2_DIR / "ninja_mod"
    
    parallel_jobs = multiprocessing.cpu_count()
    
    content = f'''# Generated by bootstrap.py
# Bootstrap epoch: {BOOTSTRAP_EPOCH}
# Stage 3: Final build with ninja_stage2

# Variables
builddir = {out_rel}
builddir_final = {build_rel}
ninja_src = {ninja_mod_rel}
stage2_dir = {stage2_rel}
stage3_dir = {stage3_rel}

# =============================================================================
# Stage 3: Build ninja_stage3 and ninja_monitor with ninja_stage2
# =============================================================================

# Download Go dependencies
rule go_mod_download
  command = go mod download
  description = GO MOD DOWNLOAD

build .go_deps: go_mod_download

# Build ninja_stage3 using cmake + ninja_stage2
rule cmake_configure_stage3
  command = cmake -S $ninja_src -B $stage3_dir -G Ninja -DCMAKE_MAKE_PROGRAM={ninja_stage2_bin_abs}
  description = STAGE3 CMAKE CONFIGURE

rule cmake_build_stage3
  command = cmake --build $stage3_dir -j {parallel_jobs}
  description = STAGE3 CMAKE BUILD

build $stage3_dir: cmake_configure_stage3
build $stage3_dir/ninja_mod: cmake_build_stage3 | $stage3_dir

# Copy ninja_mod to final build directory
rule copy_ninja
  command = mkdir -p $builddir_final && cp $stage3_dir/ninja_mod $builddir_final/ninja
  description = COPY ninja_mod to build/ninja

build $builddir_final/ninja: copy_ninja | $stage3_dir/ninja_mod

# Build ninja_monitor using go build (depends on go_deps)
rule go_build
  command = go build -o $builddir_final/ninja_monitor ./cmd/ninja_monitor
  description = GO BUILD ninja_monitor

build $builddir_final/ninja_monitor: go_build | .go_deps

# =============================================================================
# Phony targets
# =============================================================================

build all: phony $builddir_final/ninja $builddir_final/ninja_monitor

build ninja: phony $builddir_final/ninja
build ninja_monitor: phony $builddir_final/ninja_monitor

default all
'''
    
    BUILD_NINJA.write_text(content)
    EPOCH_FILE.write_text(f"{BOOTSTRAP_EPOCH}\n")
    
    logger.success(f"Generated {BUILD_NINJA.relative_to(SCRIPT_DIR)}")
    return True


def step8_ninja_stage3(logger: Logger, ninja_args: List[str]) -> bool:
    """
    Step 8: Run ninja_stage3 with ninja_monitor_gobuild.
    
    Uses ninja_monitor_gobuild to monitor ninja_stage2 building stage3.
    """
    logger.step(8, 8, "Running ninja_stage3 (ninja_monitor_gobuild + ninja_stage2)")
    
    if not NINJA_MONITOR_GOBUILD.exists():
        logger.error(f"ninja_monitor_gobuild not found: {NINJA_MONITOR_GOBUILD}")
        return False
    
    if not NINJA_STAGE2_BIN.exists():
        logger.error(f"ninja_stage2 not found: {NINJA_STAGE2_BIN}")
        return False
    
    if not BUILD_NINJA.exists():
        logger.error(f"build.ninja not found: {BUILD_NINJA}")
        return False
    
    cmd = [
        str(NINJA_MONITOR_GOBUILD),
        "--ninja", str(NINJA_STAGE2_BIN),
        "--",
        "-f", str(BUILD_NINJA),
        *ninja_args
    ]
    
    logger.info(f"Running: {' '.join(cmd)}")
    
    try:
        result = subprocess.run(cmd, cwd=SCRIPT_DIR)
        return result.returncode == 0
    except KeyboardInterrupt:
        logger.info("Interrupted by user")
        return False


def clean_build(logger: Logger) -> bool:
    """Clean all build artifacts."""
    logger.info("Cleaning build artifacts...")
    
    dirs_to_clean = [OUT_DIR]
    
    for d in dirs_to_clean:
        if d.exists():
            logger.debug(f"Removing {d}")
            shutil.rmtree(d)
    
    logger.success("Clean complete")
    return True


def check_epoch(logger: Logger) -> bool:
    """Check if bootstrap epoch has changed."""
    if EPOCH_FILE.exists():
        return True
    
    if OUT_DIR.exists():
        logger.warn("Bootstrap epoch changed, consider running with --clean")
    
    return True


def main():
    parser = argparse.ArgumentParser(
        description="Ninja Monitor Bootstrap Script (3-stage ninja bootstrap)",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Bootstrap stages:
  Stage 1: CMake builds ninja (out/ninja_stage1/ninja_mod)
  Stage 2: ninja_stage1 rebuilds ninja (out/ninja_stage2/ninja)  
  Stage 3: ninja_stage2 rebuilds ninja_monitor (out/ninja_monitor)

Examples:
  python3 bootstrap.py              # Generate build.ninja only
  python3 bootstrap.py --bootstrap  # Full bootstrap including stage3
  python3 bootstrap.py --clean      # Clean all build artifacts
"""
    )
    
    parser.add_argument("--bootstrap", action="store_true",
        help="Run full bootstrap including stage3 (default: generate only)")
    parser.add_argument("--clean", action="store_true",
        help="Clean all build artifacts before building")
    parser.add_argument("--verbose", "-v", action="store_true",
        help="Enable verbose output")
    parser.add_argument("--skip-go", action="store_true", help="Skip go build")
    parser.add_argument("--skip-submodule", action="store_true", help="Skip submodule update")
    parser.add_argument("--skip-patch", action="store_true", help="Skip patch")
    parser.add_argument("--skip-stage1", action="store_true", help="Skip ninja_stage1")
    parser.add_argument("--skip-stage2", action="store_true", help="Skip ninja_stage2")
    parser.add_argument("--skip-monitor", action="store_true", help="Skip ninja_monitor rebuild")
    parser.add_argument("--skip-generate", action="store_true", help="Skip build.ninja generation")
    parser.add_argument("ninja_args", nargs="*", help="Args to pass to ninja (after --)")
    
    args = parser.parse_args()
    logger = Logger(verbose=args.verbose)
    
    if args.clean:
        return 0 if clean_build(logger) else 1
    
    check_epoch(logger)
    OUT_DIR.mkdir(parents=True, exist_ok=True)
    
    # Default mode: only generate build.ninja
    # --bootstrap mode: run full build process
    if not args.bootstrap:
        logger.info("Generating build.ninja only (use --bootstrap to run full build)")
        if not step7_generate_build_ninja(logger):
            logger.error("Failed to generate build.ninja")
            return 1
        logger.success("build.ninja generated successfully!")
        return 0
    
    # Full bootstrap mode: execute all steps
    total_steps = 8
    
    # Step 1: go build (ninja_monitor_gobuild)
    if not args.skip_go:
        if not step1_go_build(logger):
            logger.error("Failed at step 1: go build")
            return 1
    else:
        logger.info("Skipping step 1: go build")
    
    # Step 2: submodule update
    if not args.skip_submodule:
        if not step2_submodule_update(logger):
            logger.error("Failed at step 2: submodule update")
            return 1
    else:
        logger.info("Skipping step 2: submodule update")
    
    # Step 3: patch ninja
    if not args.skip_patch:
        if not step3_patch_ninja(logger):
            logger.error("Failed at step 3: patch ninja")
            return 1
    else:
        logger.info("Skipping step 3: patch ninja")
    
    # Step 4: ninja_stage1 (CMake)
    if not args.skip_stage1:
        if not step4_ninja_stage1(logger):
            logger.error("Failed at step 4: ninja_stage1")
            return 1
    else:
        logger.info("Skipping step 4: ninja_stage1")
    
    # Step 5: ninja_stage2 (self-hosting)
    if not args.skip_stage2:
        if not step5_ninja_stage2(logger):
            logger.error("Failed at step 5: ninja_stage2")
            return 1
    else:
        logger.info("Skipping step 5: ninja_stage2")
    
    # Step 6: ninja_monitor (rebuild with ninja)
    if not args.skip_monitor:
        if not step6_ninja_monitor(logger):
            logger.error("Failed at step 6: ninja_monitor")
            return 1
    else:
        logger.info("Skipping step 6: ninja_monitor")
    
    # Step 7: generate build.ninja
    if not args.skip_generate:
        if not step7_generate_build_ninja(logger):
            logger.error("Failed at step 7: generate build.ninja")
            return 1
    else:
        logger.info("Skipping step 7: generate build.ninja")
    
    # Step 8: ninja_stage3 (only with --bootstrap)
    if not step8_ninja_stage3(logger, args.ninja_args):
        logger.error("Failed at step 8: ninja_stage3")
        return 1
    
    logger.success("Bootstrap completed successfully!")
    return 0


if __name__ == "__main__":
    sys.exit(main())
