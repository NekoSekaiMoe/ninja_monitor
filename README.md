# Ninja Monitor

A build progress monitor for [Ninja](https://ninja-build.org/) written in Go. It provides real-time build status visualization with an action table, progress percentage, and time estimation.

## Why a Custom Ninja Build?

This project requires a **modified version of Ninja** from the Android Open Source Project:

```
https://android.googlesource.com/platform/external/ninja
```

**The reason:** Standard Ninja does not expose its internal build state to external tools. The Android fork adds a **protobuf-based frontend interface** (`--frontend_file` flag) that streams build events through a FIFO pipe, including:

- Total number of build edges
- Edge start/finish events with timing and resource usage
- Build success/failure status
- Critical path time for accurate time estimation

Without this extension, there would be no way to implement real-time progress monitoring with time estimation. The patch in `dep/i.patch` further adds CMake build support to this Android Ninja fork.

## Why Google LLC Copyright Headers?

The Go source files carry Google copyright headers because this project's core status management code was **ported from Android's Soong build system** (`soong_ui`).

Specifically:
- The `internal/status` package is derived from Soong's status tracking infrastructure
- The `internal/terminal` package is derived from Soong's terminal output formatting code
- The protobuf schema (`frontend.proto`) originates from the Ninja frontend interface in AOSP

These components were originally developed by Google for Android builds and are licensed under Apache 2.0. We preserve the original copyright notices in accordance with the license terms.

## Features

- Real-time build progress display with action table
- Time estimation based on critical path analysis
- Smart terminal output with ANSI colors
- Simple output mode for non-TTY environments
- Customizable progress format string
- Resource usage statistics (CPU time, memory, I/O)

## Requirements

- Go 1.23+
- CMake 3.15+ (for building Ninja)
- Python 3 (for bootstrap script)
- Git (for submodule management)

## Building

### Quick Build (Go only)

```bash
go build -o build/ninja_monitor ./cmd/ninja_monitor
```

### Full Bootstrap Build

The bootstrap script performs a 3-stage self-hosting build:

```bash
# Generate build.ninja only
python3 bootstrap.py

# Full build including Ninja
python3 bootstrap.py --bootstrap

# Clean all build artifacts
python3 bootstrap.py --clean
```

Bootstrap stages:
1. Build `ninja_monitor` with `go build`
2. Initialize Ninja submodule and apply CMake patch
3. Build Ninja Stage 1 with CMake
4. Rebuild Ninja Stage 2 using Ninja Stage 1 (self-hosting)
5. Generate `build.ninja` for Stage 3
6. Run final build with monitoring

## Usage

```bash
# Normal mode (invokes Ninja automatically)
./build/ninja_monitor [ninja_args...]

# Specify Ninja executable
./build/ninja_monitor --ninja=/path/to/ninja

# Monitor-only mode (read from existing FIFO)
./build/ninja_monitor --fifo=/tmp/.ninja_fifo

# Quiet mode (disable smart status table)
./build/ninja_monitor --quiet

# Custom progress format
./build/ninja_monitor --format="[%p %f/%t] "

# Custom action table height (0=auto, negative=disable)
./build/ninja_monitor --table-height=5
```

### Format Placeholders

| Placeholder | Description |
|-------------|-------------|
| `%s` | Started actions |
| `%t` | Total actions |
| `%r` | Running actions |
| `%f` | Finished actions |
| `%p` | Percentage complete |
| `%e` | Elapsed time (seconds) |
| `%l` | Estimated remaining time |

## Project Structure

```
ninja_monitor/
├── cmd/ninja_monitor/main.go   # Main entry point
├── internal/
│   ├── logger/                 # Logging interface
│   ├── ninja_frontend/         # Protobuf protocol definition
│   ├── status/                 # Status multiplexer and Ninja reader
│   └── terminal/               # Terminal output (smart/simple)
├── dep/
│   ├── ninja_mod/              # Ninja submodule (Android fork)
│   └── i.patch                 # CMake build support patch
├── bootstrap.py                # Bootstrap build script
└── go.mod                      # Go module definition
```

## Testing

```bash
go test ./...
```

## Environment Variables

| Variable | Description |
|----------|-------------|
| `SOONG_UI_TABLE_HEIGHT` | Action table height (0=auto, negative=disable) |

## License

Apache License 2.0

## Acknowledgments

- [Ninja Build System](https://ninja-build.org/)
- [Android Open Source Project](https://source.android.com/) - for the Ninja frontend interface and status management code
