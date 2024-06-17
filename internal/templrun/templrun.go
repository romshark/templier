package templrun

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/romshark/templier/internal/state"
)

// RunWatch starts `templ generate --log-level debug --watch` and reads its
// stdout pipe for failure and success logs updating the state accordingly.
// When ctx is canceled the interrupt signal is sent to the watch process
// and graceful shutdown is awaited.
func RunWatch(ctx context.Context, workDir string, st *state.Tracker) error {
	// Don't use CommandContext since it will kill the process
	// which we don't want. We want the command to finish.
	cmd := exec.Command("templ", "generate", "--log-level", "debug", "--watch")
	cmd.Dir = workDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("obtaining stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting: %w", err)
	}

	done := make(chan error, 1)
	go func() {
		// Read the command output
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			b := scanner.Bytes()
			fmt.Println("🎩", scanner.Text())
			switch {
			case bytes.HasPrefix(b, bytesPrefixErr):
				st.SetErrTempl(scanner.Text())
			case bytes.HasPrefix(b, bytesPrefixErrCleared):
				st.SetErrTempl("")
			}
		}
		if err := scanner.Err(); err != nil {
			fmt.Printf("🤖 ERR: scanning templ watch output: %v\n", err)
		}
		done <- cmd.Wait()
	}()

	select {
	case <-ctx.Done(): // Terminate templ watch gracefully.
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			return fmt.Errorf("🤖 ERR: interrupting templ watch process: %w", err)
		}
		if err := <-done; err != nil {
			return fmt.Errorf("🤖 ERR: process did not exit cleanly: %w", err)
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

// func RunGen(ctx context.Context, workDir string) (string, error) {
// 	c := exec.CommandContext(ctx, "templ", "generate")
// 	c.Dir = workDir
// 	var outbuf bytes.Buffer
// 	c.Stdout = &outbuf
// 	return outbuf.String(), c.Run()
// }
