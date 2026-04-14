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

package status

import (
	"bufio"   // bufio provides buffered I/O operations for efficient reading from files/buffers
	"fmt"     // fmt provides string formatting and error message construction
	"io"      // io provides basic I/O interfaces like EOF, ReadFull, etc.
	"os"      // os provides operating system functions like file operations (Open, Remove)
	"runtime" // runtime provides runtime information like NumCPU for getting CPU core count
	"syscall" // syscall provides low-level system calls like Mkfifo for creating named pipes
	"time"    // time provides time-related functionality like Duration and timers

	"google.golang.org/protobuf/proto" // protobuf library for parsing protobuf-encoded messages

	"ninja_monitor/internal/logger"         // project's internal logger for error and debug output
	"ninja_monitor/internal/ninja_frontend" // ninja frontend protobuf message definitions
)

// NewNinjaReader creates a new NinjaReader instance that reads status information from the ninja build system.
// It creates a named pipe (FIFO) at the specified path; ninja will write status information to this pipe.
// The NinjaReader then reads from this pipe, parses protobuf messages, and updates the UI accordingly.
//
// Parameters:
//   - ctx: Logger instance for outputting errors and debug information
//   - status: ToolStatus interface for receiving ninja status updates and updating the UI
//   - fifo: Path to the named pipe (FIFO) that will be created for communication with ninja
//
// Returns:
//   - *NinjaReader: A pointer to the newly created NinjaReader instance
//
// How it works:
//  1. First removes any existing FIFO with the same name to ensure clean state
//  2. Creates a new named pipe using syscall.Mkfifo with 0666 permissions (read/write for all)
//  3. Initializes the NinjaReader struct with necessary channels and maps
//  4. Starts the main reading goroutine (run method) which will process ninja's output
//  5. Returns immediately without blocking - the reading happens asynchronously
func NewNinjaReader(ctx logger.Logger, status ToolStatus, fifo string) *NinjaReader {
	// First remove any existing FIFO with the same name to ensure we create a fresh pipe
	// This prevents "file exists" errors if a previous run left the pipe behind
	os.Remove(fifo)

	// Create a named pipe (FIFO) using syscall.Mkfifo
	// A named pipe is a special file type that allows two unrelated processes to communicate
	// through the filesystem - one process opens it for writing, another for reading
	// 0666 permissions give all users read and write access
	if err := syscall.Mkfifo(fifo, 0666); err != nil {
		ctx.Fatalf("Failed to mkfifo(%q): %v", fifo, err)
	}

	// Create the NinjaReader struct with all necessary components
	n := &NinjaReader{
		status:     status,                   // Save ToolStatus interface for later status updates
		fifo:       fifo,                     // Save the FIFO path for opening later
		forceClose: make(chan bool),          // Channel for signaling forced shutdown (timeout case)
		done:       make(chan bool),          // Channel for signaling that shutdown is complete
		cancelOpen: make(chan bool),          // Channel for canceling the FIFO open operation
		running:    make(map[uint32]*Action), // Map to track in-progress build actions (action ID -> Action)
	}

	// Start the main reading loop in a separate goroutine
	// This is non-blocking - the function returns immediately while reading continues asynchronously
	// Multiple goroutines work together:
	//   - Main goroutine: processes status messages and updates UI
	//   - Reader goroutine: reads raw bytes from FIFO and parses protobuf messages
	//   - Opener goroutine: handles blocking FIFO open operation with cancellation support
	go n.run()

	return n
}

// NinjaReader is the main struct responsible for reading status information from the ninja build system
// and updating the UI accordingly. It communicates with ninja through a named pipe (FIFO) using
// protobuf-encoded messages.
//
// Communication Pattern:
//   - Ninja writes protobuf messages to the FIFO (stdout of ninja subprocess)
//   - This reader reads from the FIFO, parses messages, and updates UI via ToolStatus
//
// The reader uses multiple goroutines for efficient and cancellable operation:
//   - run(): Main loop that processes status messages
//   - Reader goroutine: Reads and parses protobuf from FIFO
//   - Opener goroutine: Opens FIFO with cancellation support
type NinjaReader struct {
	status       ToolStatus         // ToolStatus interface for updating UI and displaying status information
	fifo         string             // Path to the named pipe file used for communication with ninja
	forceClose   chan bool          // Channel for forcing immediate closure (used when Close() times out)
	done         chan bool          // Channel to signal that shutdown is complete
	cancelOpen   chan bool          // Channel to cancel the FIFO open operation (if Close is called before ninja connects)
	running      map[uint32]*Action // Map of currently executing build actions, keyed by action ID (uint32)
	hasAnyOutput bool               // Flag indicating whether any build command produced stdout/stderr output
}

// NINJA_READER_CLOSE_TIMEOUT defines the maximum time to wait for ninja to finish outputting
// and for the reading goroutines to complete gracefully before forcing closure.
// This timeout prevents the Close() method from blocking indefinitely if ninja hangs.
const NINJA_READER_CLOSE_TIMEOUT = 5 * time.Second

// Close gracefully shuts down the NinjaReader, waiting for all pending build actions to complete
// or for a timeout to occur. It coordinates with the running goroutines to ensure clean shutdown.
//
// How it works:
//  1. Signals cancellation to stop any blocking FIFO open operations
//  2. Waits up to 5 seconds for the reading goroutine to finish naturally (receive done signal)
//  3. If timeout occurs, sends forceClose signal to break out of the main loop
//  4. Waits another 5 seconds for graceful shutdown after forceClose
//  5. Returns without error even if forced shutdown was necessary
//
// The method ensures that:
//   - No in-progress build actions are abandoned without notification
//   - All buffered messages are processed before shutdown
//   - The application doesn't hang if ninja doesn't close its end of the pipe
func (n *NinjaReader) Close() {
	// Close cancelOpen channel to signal any blocked goroutine to give up opening the FIFO
	// This ensures Close() can return even if ninja never opens the write end
	close(n.cancelOpen)

	closed := false

	// Ninja should have exited or been killed, wait up to 5 seconds for FIFO to close and remaining messages to be processed
	// Use a select statement to implement non-blocking wait with timeout
	timeoutCh := time.After(NINJA_READER_CLOSE_TIMEOUT)
	select {
	case <-n.done:
		// Successfully received completion signal from the reading goroutine
		closed = true
	case <-timeoutCh:
		// Timeout expired - reading goroutine hasn't finished yet
	}

	// If the first wait timed out, attempt forced closure
	if !closed {
		// Force close the reading loop even if FIFO hasn't closed naturally
		// This breaks out of the select in the run() method
		close(n.forceClose)

		// Wait again for the reading thread to acknowledge closure
		// After forceClose, the run() method should exit and signal done
		timeoutCh = time.After(NINJA_READER_CLOSE_TIMEOUT)
		select {
		case <-n.done:
			closed = true
		case <-timeoutCh:
			// Timeout again - give up waiting, assume it won't send any more content
		}
	}

	// When ninja exits, we don't print error messages about running actions
	// because ninja already outputs "build stopped: interrupted by user"
}

// run is the main execution loop of NinjaReader, running in a dedicated goroutine.
// It handles all communication with ninja through the FIFO, including:
//   - Opening the named pipe for reading
//   - Spawning a goroutine to read and parse protobuf messages
//   - Processing status messages and updating the UI
//
// The method uses channel-based communication to coordinate between multiple goroutines:
//   - fileCh: Receives the opened file handle from the opener goroutine
//   - msgChan: Receives parsed Status messages from the reader goroutine
//   - forceClose: Receives signal to force immediate shutdown
//
// Message Flow:
//  1. Start opener goroutine to open FIFO (allows cancellation)
//  2. Wait for either FIFO open or cancellation signal
//  3. Spawn reader goroutine that reads bytes, parses varint + protobuf, sends to msgChan
//  4. Main loop processes messages from msgChan, updates UI via status interface
//  5. Handle forceClose signal to break out of processing loop
//  6. Signal done channel when shutdown is complete
func (n *NinjaReader) run() {
	// Use defer to ensure done channel is closed when the method exits
	// This signals to the Close() method that shutdown is complete
	defer close(n.done)

	// Opening a named pipe can block indefinitely - if ninja never opens the write end,
	// the read end will wait forever. To allow cancellation, we open in a goroutine.
	fileCh := make(chan *os.File)
	go func() {
		// Attempt to open the named pipe for reading
		// This will block until ninja opens the write end
		f, err := os.Open(n.fifo)
		if err != nil {
			// If open fails, report error via status interface and close channel
			n.status.Error(fmt.Sprintf("Failed to open fifo: %v", err))
			close(fileCh)
			return
		}
		// Successfully opened - send file handle to the channel
		fileCh <- f
	}()

	var f *os.File

	// Wait for either the pipe to be opened successfully or cancellation signal
	select {
	case f = <-fileCh:
		// Pipe opened successfully, proceed with reading
	case <-n.cancelOpen:
		// Received cancellation signal (from Close() being called)
		// Exit immediately without reading
		return
	}

	// Use defer to ensure the file is closed when the function exits
	// This releases the file descriptor and allows ninja to exit if still running
	defer f.Close()

	// Create a buffered reader for efficient I/O
	// Buffered reading reduces the number of system calls by reading large chunks into memory
	r := bufio.NewReader(f)

	// Create a channel for passing status messages from reader goroutine to main loop
	msgChan := make(chan *ninja_frontend.Status)

	// Spawn a goroutine to read raw bytes from ninja and decode protobuf messages
	// This allows the main loop to concurrently listen on multiple channels
	// while reading happens in parallel
	go func() {
		// Close msgChan when this goroutine exits to signal no more messages
		defer close(msgChan)
		for {
			// First read a varint-encoded message length from the stream
			// Each message is preceded by its length as a protobuf varint
			size, err := readVarInt(r)
			if err != nil {
				if err != io.EOF {
					// Report error if it's not the expected end-of-file
					n.status.Error(fmt.Sprintf("Got error reading from ninja: %s", err))
				}
				return
			}

			// Allocate buffer for the exact message size
			buf := make([]byte, size)
			// Read the full message data
			// io.ReadFull ensures we read exactly 'size' bytes or returns error
			_, err = io.ReadFull(r, buf)
			if err != nil {
				if err == io.EOF {
					// Incomplete data - ninja closed pipe before sending full message
					n.status.Print(fmt.Sprintf("Missing message of size %d from ninja\n", size))
				} else {
					n.status.Error(fmt.Sprintf("Got error reading from ninja: %s", err))
				}
				return
			}

			// Parse the protobuf message from the raw bytes
			// protobuf.Unmarshal deserializes the binary data into a Status struct
			msg := &ninja_frontend.Status{}
			err = proto.Unmarshal(buf, msg)
			if err != nil {
				// Invalid protobuf - report but continue reading next message
				n.status.Print(fmt.Sprintf("Error reading message from ninja: %v", err))
				continue
			}

			// Send the parsed message to the main loop via channel
			// The main loop will process this message and update UI
			msgChan <- msg
		}
	}()

	// Main processing loop: handles all ninja status messages
	// Uses select to listen on multiple channels concurrently
	for {
		var msg *ninja_frontend.Status
		var msgOk bool
		select {
		case <-n.forceClose:
			// Close() was called but reading goroutine didn't receive EOF within 5 seconds
			// Break out of the loop to force shutdown
			break
		case msg, msgOk = <-msgChan:
			// Received a message from the reader goroutine, or channel was closed
		}

		// If msgOk is false, the channel was closed (reader goroutine exited)
		// This means ninja has finished sending output
		if !msgOk {
			break
		}

		// Process BuildStarted message: Signals the beginning of a ninja build
		// Contains build configuration info used to estimate completion time
		if msg.BuildStarted != nil {
			// Get the parallelism (max parallel jobs) - if not set by ninja, use CPU core count
			parallelism := uint32(runtime.NumCPU())
			if msg.BuildStarted.GetParallelism() > 0 {
				parallelism = msg.BuildStarted.GetParallelism()
			}
			// Update UI to show the maximum parallel tasks that will run
			n.status.SetMaxParallelism(int(parallelism))

			// Estimate duration using two methods and take the longer one:
			// Method 1: Total time / parallelism (good for heavily-parallel builds)
			// This assumes all workers are constantly busy
			estimatedDurationFromTotal := time.Duration(msg.BuildStarted.GetEstimatedTotalTime()/parallelism) * time.Millisecond
			// Method 2: Critical path time (good for small builds)
			// The minimum possible time if all dependencies were perfectly parallelized
			estimatedDurationFromCriticalPath := time.Duration(msg.BuildStarted.GetCriticalPathTime()) * time.Millisecond
			// Choose the longer estimate as the final estimate
			// This provides a conservative upper bound
			estimatedDuration := max(estimatedDurationFromTotal, estimatedDurationFromCriticalPath)

			// If we have a valid estimate, set the expected completion time
			if estimatedDuration > 0 {
				n.status.SetEstimatedTime(time.Now().Add(estimatedDuration))
			}
		}

		// Process TotalEdges message: Sets the total number of build actions (edges) to execute
		// Tells the UI the total number of build steps needed to complete
		if msg.TotalEdges != nil {
			n.status.SetTotalActions(int(msg.TotalEdges.GetTotalEdges()))
		}

		// Process EdgeStarted message: Signals that a build action has started
		// An "edge" in ninja terminology is a build rule/dependency edge (a single build step)
		// When this message arrives, a build command is about to execute
		if msg.EdgeStarted != nil {
			// Create Action struct with details about this build step
			action := &Action{
				Description:   msg.EdgeStarted.GetDesc(),     // Human-readable description of the action
				Outputs:       msg.EdgeStarted.Outputs,       // List of output files produced by this action
				Inputs:        msg.EdgeStarted.Inputs,        // List of input files this action depends on
				Command:       msg.EdgeStarted.GetCommand(),  // The actual command line to execute
				ChangedInputs: msg.EdgeStarted.ChangedInputs, // Input files that have actually changed
			}
			// Notify UI that this action is starting to execute
			// The UI typically displays this in a progress view
			n.status.StartAction(action)
			// Store in running map so we can associate the completion message later
			// Key is the unique action ID, value is the Action struct
			n.running[msg.EdgeStarted.GetId()] = action
		}

		// Process EdgeFinished message: Signals that a build action has completed
		// Contains execution results including exit status, output, and performance stats
		if msg.EdgeFinished != nil {
			// Look up the corresponding EdgeStarted message using action ID
			// This pairs the start and end of this build action
			if started, ok := n.running[msg.EdgeFinished.GetId()]; ok {
				// Remove from running map since action is complete
				delete(n.running, msg.EdgeFinished.GetId())

				var err error
				// Check exit code to determine if command succeeded
				exitCode := int(msg.EdgeFinished.GetStatus())
				if exitCode != 0 {
					err = fmt.Errorf("exited with code: %d", exitCode)
				}

				// Get the command's stdout/stderr output
				rawOutput := msg.EdgeFinished.GetOutput()
				// Notify UI that action completed, passing result and statistics
				n.status.FinishAction(ActionResult{
					Action: started,   // The action that started (for display context)
					Output: rawOutput, // Command output (stdout/stderr)
					Error:  err,       // Error if non-zero exit code
					Stats: ActionResultStats{ // Performance statistics from the command
						UserTime:                   msg.EdgeFinished.GetUserTime(),                   // User-mode CPU time in milliseconds
						SystemTime:                 msg.EdgeFinished.GetSystemTime(),                 // Kernel-mode CPU time in milliseconds
						MaxRssKB:                   msg.EdgeFinished.GetMaxRssKb(),                   // Maximum resident set size in KB
						MinorPageFaults:            msg.EdgeFinished.GetMinorPageFaults(),            // Minor page faults (page reclaims)
						MajorPageFaults:            msg.EdgeFinished.GetMajorPageFaults(),            // Major page faults (page loads from disk)
						IOInputKB:                  msg.EdgeFinished.GetIoInputKb(),                  // Input I/O in kilobytes
						IOOutputKB:                 msg.EdgeFinished.GetIoOutputKb(),                 // Output I/O in kilobytes
						VoluntaryContextSwitches:   msg.EdgeFinished.GetVoluntaryContextSwitches(),   // Voluntary context switches (阻塞等待I/O等)
						InvoluntaryContextSwitches: msg.EdgeFinished.GetInvoluntaryContextSwitches(), // Involuntary context switches (时间片用完等)
						Tags:                       msg.EdgeFinished.GetTags(),                       // Build system tags/categories
					},
				})

				// Update hasAnyOutput flag if this command produced any output
				// Used to determine whether to show output summary after build
				n.hasAnyOutput = n.hasAnyOutput || len(rawOutput) > 0
			}
		}

		// Process Message message: Text messages from ninja
		// Ninja uses this for status messages, warnings, errors, and debug info
		if msg.Message != nil {
			// Prepend "ninja: " to identify the source of the message
			message := "ninja: " + msg.Message.GetMessage()
			// Handle message according to its severity level
			switch msg.Message.GetLevel() {
			case ninja_frontend.Status_Message_INFO:
				// Info level: Display briefly in status bar
				n.status.Status(message)
			case ninja_frontend.Status_Message_WARNING:
				// Warning level: Display as warning (may show in different color)
				n.status.Print("warning: " + message)
			case ninja_frontend.Status_Message_ERROR:
				// Error level: Display as error (may show in different color)
				n.status.Error(message)
			case ninja_frontend.Status_Message_DEBUG:
				// Debug level: Only display in verbose/debug mode
				n.status.Verbose(message)
			default:
				// Unknown level: Print normally
				n.status.Print(message)
			}
		}

		// Process BuildFinished message: Signals the end of a ninja build
		// This is the final message indicating build is complete (success or failure)
		if msg.BuildFinished != nil {
			// Notify UI that build has finished
			// UI typically clears progress display and shows summary
			n.status.Finish()
		}
	}
}

// HasAnyOutput returns true if any build command executed by ninja produced stdout or stderr output.
// This is used to determine whether to display output summary after build completion.
//
// IMPORTANT: This method must be called AFTER Close() is called to ensure all commands have been processed.
// Calling it while build is still in progress may return incorrect results.
func (n *NinjaReader) HasAnyOutput() bool {
	return n.hasAnyOutput
}

// readVarInt reads a protobuf-style variable-length integer (varint) from a buffered reader.
//
// Varint Encoding (Variable-length Integer):
// -------------------------------
// Varint is a method of encoding integers using a variable number of bytes.
// It's used in protobuf to save space - small numbers use fewer bytes.
//
// Encoding scheme:
//   - Each byte uses 7 bits for data, 1 bit for continuation flag
//   - Lower 7 bits (0-6): Store the actual integer value
//   - Bit 7 (highest bit): Continuation bit
//   - 0 = this is the last byte
//   - 1 = more bytes follow
//
// Examples:
//   - 300 in varint: 10101100 00000010 = 0xAC 0x02
//   - First byte: 10101100 = 0xAC, continuation=1
//   - Second byte: 00000010 = 0x02, continuation=0
//   - Value: (0xAC & 0x7F) | (0x02 << 7) = 44 | 256 = 300
//   - 1 in varint: 00000001 = 0x01
//   - Single byte, continuation=0, value=1
//
// Parameter:
//   - r: Buffered reader to read bytes from
//
// Returns:
//   - int: The decoded integer value
//   - error: Any error encountered during reading (including EOF)
//
// How it works:
//  1. Read bytes one at a time in a loop
//  2. Extract lower 7 bits and shift left by (shift * 7) to accumulate value
//  3. If highest bit is 0, this is the last byte - we're done
//  4. If highest bit is 1, continue to next byte
//  5. Increment shift for each byte processed
//  6. If shift exceeds 4, this is an invalid varint32 (would overflow)
func readVarInt(r *bufio.Reader) (int, error) {
	ret := 0         // Accumulator for the final result
	shift := uint(0) // Bit shift amount (multiplied by 7 for each byte)

	for {
		// Read a single byte from the stream
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}

		// Extract lower 7 bits (mask 0x7F) and shift left by appropriate amount
		// First byte contributes bits 0-6, second byte bits 7-13, etc.
		// This builds up the integer by adding each byte's contribution
		ret += int(b&0x7f) << (shift * 7)
		// If highest bit is 0, this is the last byte - we're done reading
		if b&0x80 == 0 {
			break
		}
		// Continuation bit is set - more bytes follow
		shift += 1
		// varint32 uses maximum 4 bytes (32 bits / 7 bits per byte ≈ 4.6, so 5 max but 4 for valid data)
		// If we've read more than 4 bytes, the data is malformed
		if shift > 4 {
			return 0, fmt.Errorf("Expected varint32 length-delimited message")
		}
	}

	return ret, nil
}
