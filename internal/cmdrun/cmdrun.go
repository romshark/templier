package cmdrun

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/romshark/templier/internal/log"
	"github.com/romshark/templier/internal/statetrack"
)

var ErrExitCode1 = errors.New("exit code 1")

// Run runs an arbitrary command and returns (output, ErrExitCode1)
// if it exits with error code 1, otherwise returns the original error.
func Run(
	ctx context.Context, workDir string, envVars []string, cmd string, args ...string,
) (out []byte, err error) {
	c := exec.CommandContext(ctx, cmd, args...)
	c.Dir = workDir

	if envVars != nil {
		c.Env = append(os.Environ(), envVars...)
	}

	log.Debugf("running command: %s", c.String())
	out, err = c.CombinedOutput()
	if exitError, ok := err.(*exec.ExitError); ok && exitError.ExitCode() == 1 {
		log.Debugf("running command (pid: %d): exited with exit code 1", c.Process.Pid)
		return out, ErrExitCode1
	} else if err != nil {
		return nil, err
	}
	return out, nil
}

// Sh runs an arbitrary shell script and behaves similar to Run.
func Sh(ctx context.Context, workDir string, sh string) (out []byte, err error) {
	return Run(ctx, workDir, nil, "sh", "-c", sh)
}

// RunTemplFmt runs `templ fmt <path>`.
func RunTemplFmt(ctx context.Context, workDir string, path string) error {
	cmd := exec.Command("templ", "fmt", "-fail", path)
	cmd.Dir = workDir
	return cmd.Run()
}

// RunTemplWatch starts `templ generate --log-level debug --watch` and reads its
// stdout pipe for failure and success logs updating the state accordingly.
// When ctx is canceled the interrupt signal is sent to the watch process
// and graceful shutdown is awaited.
func RunTemplWatch(ctx context.Context, workDir string, st *statetrack.Tracker) error {
	// Don't use CommandContext since it will kill the process
	// which we don't want. We want the command to finish.
	cmd := exec.Command("templ", "generate", "--log-level", "debug", "--watch")
	cmd.Dir = workDir

	stdout, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("obtaining stdout pipe: %w", err)
	}

	log.Debugf("starting a-h/templ in the background: %s", cmd.String())
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting: %w", err)
	}

	done := make(chan error, 1)
	go func() {
		// Read the command output
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			b := scanner.Bytes()
			log.Debugf("templ: %s", string(b))
			switch {
			case bytes.HasPrefix(b, bytesPrefixErr):
				st.Set(statetrack.IndexTempl, scanner.Text())
			case bytes.HasPrefix(b, bytesPrefixErrCleared):
				st.Set(statetrack.IndexTempl, "")
			}
		}
		if err := scanner.Err(); err != nil {
			log.Errorf("scanning templ watch output: %v", err)
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

var (
	bytesPrefixErr        = []byte(`(✗) Error generating code`)
	bytesPrefixErrCleared = []byte(`(✓) Error cleared`)
)
