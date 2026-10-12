package cmdrun_test

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
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

const (
	// templHelperEnv makes TestTemplHelperProcess act as the templ CLI.
	templHelperEnv = "TEMPLIER_CMDRUN_TEMPL_HELPER"

	helperTemplVersion  = "v0.0.0-helper"
	helperGeneratedFile = "helper-generated"
)

// TestTemplHelperProcess isn't a real test. It's executed as a child process by
// the RunTempl* tests and imitates the templ CLI, failing on unexpected arguments.
func TestTemplHelperProcess(t *testing.T) {
	if os.Getenv(templHelperEnv) != "1" {
		t.Skip("not running as templ helper process")
	}
	args := flag.Args() // The arguments following "--".
	switch {
	case slices.Equal(args, []string{"version"}):
		fmt.Println(helperTemplVersion)
	case slices.Equal(args, []string{"generate"}):
		// Leave proof of the invocation in the working directory.
		if err := os.WriteFile(helperGeneratedFile, nil, 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case len(args) == 3 && args[0] == "fmt" && args[1] == "-fail":
	default:
		fmt.Fprintf(os.Stderr, "helper: unexpected arguments %q\n", args)
		os.Exit(2)
	}
	os.Exit(0)
}

// templHelperCmd returns a templ command running TestTemplHelperProcess,
// which receives all arguments appended to it.
func templHelperCmd(t *testing.T) []string {
	t.Helper()
	t.Setenv(templHelperEnv, "1")
	// Unlike os.Args[0], the executable path stays valid
	// when the command runs in a different working directory.
	exe, err := os.Executable()
	assert.NoError(t, err)
	return []string{exe, "-test.run=^TestTemplHelperProcess$", "--"}
}

func TestRunTemplVersion(t *testing.T) {
	version, err := cmdrun.RunTemplVersion(
		context.Background(), "", templHelperCmd(t), slog.New(slog.DiscardHandler),
	)
	assert.NoError(t, err)
	assert.Equal(t, helperTemplVersion, version)
}

// TestRunTemplVersionErr makes sure the error includes what the command printed
// to stderr, since that's where for example `go tool` explains why it failed.
func TestRunTemplVersionErr(t *testing.T) {
	templCmd := append(templHelperCmd(t), "unknown")
	_, err := cmdrun.RunTemplVersion(
		context.Background(), "", templCmd, slog.New(slog.DiscardHandler),
	)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), `helper: unexpected arguments ["unknown" "version"]`)
}

// TestRunTemplGenerate makes sure the configured templ command is run
// in the working directory, since it's what replaces the debug output
// when templ watch had to be terminated forcefully.
func TestRunTemplGenerate(t *testing.T) {
	workDir := t.TempDir()
	err := cmdrun.RunTemplGenerate(context.Background(), workDir, templHelperCmd(t))
	assert.NoError(t, err)

	_, err = os.Stat(filepath.Join(workDir, helperGeneratedFile))
	assert.NoError(t, err, "the templ command must have run in the working directory")
}

// TestRunTemplFmtKeepsTemplCmd makes sure the arguments aren't appended
// to the configured templ command in place since it's shared.
func TestRunTemplFmtKeepsTemplCmd(t *testing.T) {
	// Spare capacity would allow append to write past the end of templCmd.
	templCmd := slices.Grow(templHelperCmd(t), 8)
	before := slices.Clone(templCmd[:cap(templCmd)])

	err := cmdrun.RunTemplFmt(context.Background(), "", templCmd, "page.templ")
	assert.NoError(t, err)
	assert.Equal(t, before, templCmd[:cap(templCmd)])
}
