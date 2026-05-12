package cmdrun

import (
	"io"
	"log/slog"
	"testing"

	"github.com/romshark/templier/internal/statetrack"

	"github.com/stretchr/testify/require"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestHandleTemplOutputLineWarningDoesNotGateBuilds covers Bug A.
//
// Templ uses two distinct prefixes on stdout: "(!)" for warnings and
// "(✗)" for errors. The engine treats any non-empty IndexTempl as a
// fatal templ error and skips `go build` (initial and subsequent).
// Pre-fix, both prefixes were written into IndexTempl, so any chatty
// warning from templ silently disabled the entire build pipeline:
//
//   - "(!) templ version check: templ not found in go.mod file..." for
//     projects not importing a-h/templ.
//   - "(!) templ version check: generator vX is older than templ
//     version vY found in go.mod file..." for CLI vs go.mod skew.
//
// Both are FYIs, not errors. Only "(✗)" lines should populate
// IndexTempl.
func TestHandleTemplOutputLineWarningDoesNotGateBuilds(t *testing.T) {
	cases := []string{
		"(!) templ version check: templ not found in go.mod file, " +
			"run `go get github.com/a-h/templ` to install it",
		"(!) templ version check: generator v0.3.1001 is older than " +
			"templ version v0.3.1020 found in go.mod file, " +
			"consider upgrading templ CLI",
	}
	for _, line := range cases {
		t.Run(line[:20], func(t *testing.T) {
			st := statetrack.NewTracker(0)
			ch := make(chan TemplChange, 1)

			handleTemplOutputLine([]byte(line), discardLogger(), st, ch)

			require.Empty(t, st.Get(statetrack.IndexTempl),
				"warning must not populate IndexTempl — that would gate the build")
			require.Equal(t, -1, st.ErrIndex(),
				"ErrIndex must remain -1 after a warning")
		})
	}
}

func TestHandleTemplOutputLineError(t *testing.T) {
	st := statetrack.NewTracker(0)
	ch := make(chan TemplChange, 1)

	line := []byte("(✗) Error generating: ./bad.templ:1:2 syntax error")
	handleTemplOutputLine(line, discardLogger(), st, ch)

	require.Equal(t, string(line), st.Get(statetrack.IndexTempl))
	require.Equal(t, statetrack.IndexTempl, st.ErrIndex())
}

func TestHandleTemplOutputLineErrorCleared(t *testing.T) {
	st := statetrack.NewTracker(0)
	st.Set(statetrack.IndexTempl, "previous error")
	ch := make(chan TemplChange, 1)

	handleTemplOutputLine(
		[]byte("(✓) Error cleared"), discardLogger(), st, ch,
	)
	require.Empty(t, st.Get(statetrack.IndexTempl))
}

func TestHandleTemplOutputLinePostGenRestart(t *testing.T) {
	st := statetrack.NewTracker(0)
	ch := make(chan TemplChange, 1)

	handleTemplOutputLine(
		[]byte("(✓) Post-generation event received, processing... needsRestart=true"),
		discardLogger(), st, ch,
	)
	select {
	case got := <-ch:
		require.Equal(t, TemplChangeNeedsRestart, got)
	default:
		t.Fatal("expected a TemplChangeNeedsRestart signal")
	}
}

func TestHandleTemplOutputLinePostGenReload(t *testing.T) {
	st := statetrack.NewTracker(0)
	ch := make(chan TemplChange, 1)

	handleTemplOutputLine(
		[]byte("(✓) Post-generation event received, processing... needsBrowserReload=true"),
		discardLogger(), st, ch,
	)
	select {
	case got := <-ch:
		require.Equal(t, TemplChangeNeedsBrowserReload, got)
	default:
		t.Fatal("expected a TemplChangeNeedsBrowserReload signal")
	}
}
