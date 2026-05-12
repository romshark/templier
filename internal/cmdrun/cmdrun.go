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
	return Run(ctx, workDir, nil, logger, "sh", "-c", sh)
}

// RunTemplFmt runs `templ fmt <path>`.
func RunTemplFmt(ctx context.Context, workDir string, path string) error {
	cmd := exec.Command("templ", "fmt", "-fail", path)
	cmd.Dir = workDir
	return cmd.Run()
}

type TemplChange int8

const (
	_ TemplChange = iota
	TemplChangeNeedsRestart
	TemplChangeNeedsBrowserReload
)

// RunTemplWatch starts `templ generate --log-level debug --watch` and reads its
// stdout pipe for failure and success logs updating the state accordingly.
// When ctx is canceled the interrupt signal is sent to the watch process
// and graceful shutdown is awaited.
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
			handleTemplOutputLine(scanner.Bytes(), logger, st, templChange)
		}
		if err := scanner.Err(); err != nil {
			logger.Error("scanning templ watch output", "err", err)
		}
		done <- cmd.Wait()
	}()

	select {
	case <-ctx.Done(): // Terminate templ watch gracefully.
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			return fmt.Errorf("interrupting templ watch process: %w", err)
		}
		if err := <-done; err != nil {
			return fmt.Errorf("process did not exit cleanly: %w", err)
		}
	case err := <-done: // Command finished without interruption.
		return err
	}
	return nil
}

// handleTemplOutputLine classifies one line from `templ generate --watch`
// stdout and updates state / change channels accordingly.
//
// Warnings ("(!)") are intentionally not written to IndexTempl. Templ
// emits them for benign conditions (version-check FYIs, deprecation
// notes, "templ not found in go.mod" for non-templ projects, CLI vs
// go.mod version skew). The engine treats any non-empty IndexTempl as
// a hard error that gates initial build and every subsequent rebuild,
// so conflating warnings with errors silently disables templier
// whenever templ has anything chatty to say. Only "(✗)" lines are
// actual generator errors.
func handleTemplOutputLine(
	b []byte,
	logger *slog.Logger,
	st *statetrack.Tracker,
	templChange chan<- TemplChange,
) {
	logger.Debug("templ", "output", string(b))
	switch {
	case bytes.HasPrefix(b, bytesPrefixWarning):
		logger.Warn("templ", "output", string(b))
	case bytes.HasPrefix(b, bytesPrefixErr):
		st.Set(statetrack.IndexTempl, string(b))
	case bytes.HasPrefix(b, bytesPrefixErrCleared):
		st.Set(statetrack.IndexTempl, "")
	}
	if after, found := bytes.CutPrefix(b, bytesPrefixPostGenEvent); found {
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
