package cmdrun_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"testing"
	"time"

	"github.com/romshark/templier/internal/cmdrun"

	"github.com/alecthomas/assert/v2"
)

const (
	// helperEnv makes TestHelperProcess act as the process under test.
	helperEnv = "TEMPLIER_CMDRUN_HELPER"

	helperReadyMarker    = "helper: running"
	helperGracefulMarker = "helper: graceful shutdown"
)

// TestHelperProcess isn't a real test. It's executed as a child process by
// TestStopProcess and imitates an application server that shuts down
// gracefully when it's interrupted.
func TestHelperProcess(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		t.Skip("not running as helper process")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	fmt.Println(helperReadyMarker)
	select {
	case <-ctx.Done():
		fmt.Println(helperGracefulMarker)
		os.Exit(0)
	case <-time.After(30 * time.Second):
		fmt.Println("helper: never interrupted")
		os.Exit(2)
	}
}

// TestStopProcess makes sure a process started with SetProcessGroup can be stopped,
// and that it gets to run its shutdown logic whenever StopProcess reports that
// it was stopped gracefully.
func TestStopProcess(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperProcess", "-test.v")
	cmd.Env = append(os.Environ(), helperEnv+"=1")
	cmdrun.SetProcessGroup(cmd)

	stdout, err := cmd.StdoutPipe()
	assert.NoError(t, err)

	assert.NoError(t, cmd.Start())

	// Make sure the helper never outlives the test, even if it fails early.
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	lines := make(chan string, 64)
	scanErr := make(chan error, 1)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		scanErr <- scanner.Err()
	}()

	// Wait for the helper to install its signal handler,
	// otherwise the stop request could arrive before it's able to react to it.
	awaitLine(t, lines, helperReadyMarker)

	graceful, err := cmdrun.StopProcess(cmd.Process)
	assert.NoError(t, err)
	t.Logf("StopProcess reported graceful=%v", graceful)

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	select {
	case err := <-exited:
		if graceful {
			assert.NoError(t, err, "gracefully stopped process must exit cleanly")
		}
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("process didn't exit after StopProcess")
	}

	if !graceful {
		// The process had to be terminated, for example because there's no
		// console to deliver a console control event to. It never got the
		// chance to shut down, so there's nothing more to assert.
		t.Log("process was terminated instead of stopped gracefully")
		return
	}

	var received []string
	for l := range lines {
		received = append(received, l)
	}

	// cmd.Wait closes the read end of the pipe once the process is gone,
	// which can surface as os.ErrClosed instead of a clean EOF. Any other error means
	// the output was truncated and the assertion below would be meaningless.
	if err := <-scanErr; err != nil && !errors.Is(err, os.ErrClosed) {
		t.Fatalf("reading process output: %v", err)
	}

	assert.SliceContains(t, received, helperGracefulMarker,
		"a gracefully stopped process must run its shutdown logic")
}

// awaitLine blocks until the expected line was received.
func awaitLine(t *testing.T, lines <-chan string, expected string) {
	t.Helper()
	timeout := time.After(30 * time.Second)
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("output ended before %q was received", expected)
			}
			if l == expected {
				return
			}
		case <-timeout:
			t.Fatalf("timed out waiting for %q", expected)
		}
	}
}
