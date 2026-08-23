package cmdrun

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"

	"github.com/romshark/templier/internal/statetrack"
)

var ErrExitCode1 = errors.New("exit code 1")

// forceColorEnv uses common color-output env var conventions to keep ANSI
// colors enabled even when stdout/stderr is a pipe.
//
// See:
//   - https://cmake.org/cmake/help/latest/envvar/CLICOLOR_FORCE.html
//   - https://force-color.org/
//
// Templier captures that output and renders it in the browser via ansihtml.
var forceColorEnv = []string{"FORCE_COLOR=1", "CLICOLOR_FORCE=1"}

// Run runs an arbitrary command and returns (output, ErrExitCode1)
// if it exits with error code 1, otherwise returns the original error.
func Run(
	ctx context.Context, workDir string, envVars []string,
	logger *slog.Logger,
	cmd string, args ...string,
) (out []byte, err error) {
	c := exec.CommandContext(ctx, cmd, args...)
	c.Dir = workDir

	c.Env = append(os.Environ(), forceColorEnv...)
	c.Env = append(c.Env, envVars...)

	logger.Debug("running command", "cmd", c.String())
	out, err = c.CombinedOutput()
	if exitError, ok := err.(*exec.ExitError); ok && exitError.ExitCode() == 1 {
		logger.Debug("command exited with code 1", "pid", c.Process.Pid)
		return out, ErrExitCode1
	} else if err != nil {
		return nil, err
	}
	return out, nil
}

// Sh runs an arbitrary shell script and behaves similar to Run.
func Sh(ctx context.Context, workDir string, logger *slog.Logger, sh string) (out []byte, err error) {
	name, args := shellCommand(sh)
	return Run(ctx, workDir, nil, logger, name, args...)
}

// ShellName returns the name of the shell executable Sh runs scripts with.
func ShellName() string {
	name, _ := shellCommand("")
	return name
}

// RunTemplFmt runs `templ fmt <path>`.
func RunTemplFmt(ctx context.Context, workDir string, path string) error {
	cmd := exec.Command("templ", "fmt", "-fail", path)
	cmd.Dir = workDir
	return cmd.Run()
}

// RunTemplGenerate runs `templ generate` writing production output.
func RunTemplGenerate(ctx context.Context, workDir string) error {
	cmd := exec.CommandContext(ctx, "templ", "generate")
	cmd.Dir = workDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, string(out))
	}
	return nil
}

type TemplChange int8

const (
	_ TemplChange = iota
	TemplChangeNeedsRestart
	TemplChangeNeedsBrowserReload
)

// RunTemplWatch starts `templ generate --log-level debug --watch` and reads its
// stdout pipe for failure and success logs updating the state accordingly.
// When ctx is canceled the watch process is stopped and its exit is awaited.
// If it had to be terminated forcefully the production output is generated explicitly,
// because the terminated process never got the chance to write it.
func RunTemplWatch(
	ctx context.Context,
	workDir string,
	logger *slog.Logger,
	st *statetrack.Tracker,
	templChange chan<- TemplChange,
) error {
	// Don't use CommandContext since it will kill the process
	// which we don't want. We want the command to finish.
	cmd := exec.Command(
		"templ", "generate",
		"--watch",
		"--log-level", "debug",
		// Disable Templ's new native Go watcher to avoid any collisions
		// since Templier is already watching .go file changes.
		"--watch-pattern", `(.+\.templ$)`,
	)
	cmd.Dir = workDir
	// Required for the watch process to be stoppable gracefully on Windows.
	SetProcessGroup(cmd)

	stdout, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("obtaining stdout pipe: %w", err)
	}

	logger.Debug("starting a-h/templ in the background", "cmd", cmd.String())
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting: %w", err)
	}

	done := make(chan error, 1)
	go func() {
		// Read the command output
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			b := scanner.Bytes()
			logger.Debug("templ", "output", string(b))
			handleTemplWatchLine(b, st, templChange)
		}
		if err := scanner.Err(); err != nil {
			logger.Error("scanning templ watch output", "err", err)
		}
		done <- cmd.Wait()
	}()

	select {
	case <-ctx.Done(): // Terminate templ watch.
		graceful, err := StopProcess(cmd.Process)
		if err != nil {
			return fmt.Errorf("stopping templ watch process: %w", err)
		}
		waitErr := <-done
		if graceful {
			if waitErr != nil {
				return fmt.Errorf("process did not exit cleanly: %w", waitErr)
			}
			return nil
		}
		if waitErr == nil {
			// The watch process exited on its own before it could be
			// terminated and already wrote the production output.
			return nil
		}
		// The watch process had to be terminated forcefully and never got
		// the chance to replace the debug components with production output,
		// hence it's generated explicitly.
		// ctx is already canceled at this point and can't be used.
		if err := RunTemplGenerate(context.Background(), workDir); err != nil {
			return fmt.Errorf("generating templ production output: %w", err)
		}
	case err := <-done: // Command finished without interruption.
		return err
	}
	return nil
}

func handleTemplWatchLine(
	line []byte,
	st *statetrack.Tracker,
	templChange chan<- TemplChange,
) {
	switch {
	case bytes.HasPrefix(line, bytesPrefixWarning):
		return // Warnings must not block Go rebuilds.
	case bytes.HasPrefix(line, bytesPrefixErr):
		st.Set(statetrack.IndexTempl, string(line))
	case bytes.HasPrefix(line, bytesPrefixErrCleared):
		st.Set(statetrack.IndexTempl, "")
	}
	if after, found := bytes.CutPrefix(line, bytesPrefixPostGenEvent); found {
		switch {
		case bytes.Contains(after, bytesNeedsRestart):
			select {
			case templChange <- TemplChangeNeedsRestart:
			default:
			}
		case bytes.Contains(after, bytesNeedsBrowserReload):
			select {
			case templChange <- TemplChangeNeedsBrowserReload:
			default:
			}
		}
	}
}

var (
	bytesPrefixWarning      = []byte(`(!)`)
	bytesPrefixErr          = []byte(`(✗)`)
	bytesPrefixErrCleared   = []byte(`(✓) Error cleared`)
	bytesPrefixPostGenEvent = []byte(`(✓) Post-generation event received, processing...`)
	bytesNeedsRestart       = []byte(`needsRestart=true`)
	bytesNeedsBrowserReload = []byte(`needsBrowserReload=true`)
)
