package watcher_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/romshark/templier/internal/watcher"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/require"
)

func TestWatcher(t *testing.T) {
	base, notifications := t.TempDir(), make(chan fsnotify.Event)
	w := runNewWatcher(t, base, notifications)

	// Create a sub-directory that exists even before Run
	MustMkdir(t, base, "existing-subdir")

	require.NoError(t, w.Add(base))

	ExpectWatched(t, w, []string{
		base,
		filepath.Join(base, "existing-subdir"),
	})

	// Helper to collect events until timeout
	collectEvents := func(minCount int, timeout time.Duration) []fsnotify.Event {
		var events []fsnotify.Event
		deadline := time.After(timeout)
		for {
			select {
			case e := <-notifications:
				events = append(events, e)
				if len(events) >= minCount {
					// Drain any additional events briefly
					drainDeadline := time.After(50 * time.Millisecond)
				drain:
					for {
						select {
						case e := <-notifications:
							events = append(events, e)
						case <-drainDeadline:
							break drain
						}
					}
					return events
				}
			case <-deadline:
				return events
			}
		}
	}

	var events []fsnotify.Event

	MustCreateFile(t, base, "newfile")
	events = append(events, <-notifications)

	MustMkdir(t, base, "newdir")
	events = append(events, <-notifications)
	ExpectWatched(t, w, []string{
		base,
		filepath.Join(base, "existing-subdir"),
		filepath.Join(base, "newdir"),
	})

	MustMkdir(t, base, "newdir", "subdir")
	events = append(events, <-notifications)
	ExpectWatched(t, w, []string{
		base,
		filepath.Join(base, "existing-subdir"),
		filepath.Join(base, "newdir"),
		filepath.Join(base, "newdir", "subdir"),
	})

	MustCreateFile(t, base, "newdir", "subdir", "subfile")
	events = append(events, <-notifications)

	MustCreateFile(t, base, "newdir", "subdir", "subfile2")
	events = append(events, <-notifications)

	MustCreateFile(t, base, "existing-subdir", "subfile3")
	events = append(events, <-notifications)

	MustRemove(t, base, "existing-subdir", "subfile3")
	events = append(events, <-notifications)

	MustRemove(t, base, "existing-subdir")
	events = append(events, <-notifications)

	// Renaming may generate 1 or 2 events depending on platform
	MustRename(t, filepath.Join(base, "newdir"), filepath.Join(base, "newname"))
	renameEvents := collectEvents(1, 2*time.Second)
	require.NotEmpty(t, renameEvents, "expected at least one event for rename")
	events = append(events, renameEvents...)

	// After rename, we need to re-add the new directory to watch it
	// because the watcher removes the old path on rename
	require.NoError(t, w.Add(filepath.Join(base, "newname")))

	ExpectWatched(t, w, []string{
		base,
		filepath.Join(base, "newname"),
		filepath.Join(base, "newname", "subdir"),
	})

	// Verify expected events were received
	eventsMustContain(t, events, fsnotify.Event{
		Op:   fsnotify.Create,
		Name: filepath.Join(base, "newfile"),
	})
	eventsMustContain(t, events, fsnotify.Event{
		Op:   fsnotify.Create,
		Name: filepath.Join(base, "newdir"),
	})
	eventsMustContain(t, events, fsnotify.Event{
		Op:   fsnotify.Create,
		Name: filepath.Join(base, "newdir", "subdir"),
	})
	eventsMustContain(t, events, fsnotify.Event{
		Op:   fsnotify.Create,
		Name: filepath.Join(base, "newdir", "subdir", "subfile"),
	})
	eventsMustContain(t, events, fsnotify.Event{
		Op:   fsnotify.Create,
		Name: filepath.Join(base, "newdir", "subdir", "subfile2"),
	})
	eventsMustContain(t, events, fsnotify.Event{
		Op:   fsnotify.Create,
		Name: filepath.Join(base, "existing-subdir", "subfile3"),
	})
	eventsMustContain(t, events, fsnotify.Event{
		Op:   fsnotify.Remove,
		Name: filepath.Join(base, "existing-subdir", "subfile3"),
	})
	eventsMustContain(t, events, fsnotify.Event{
		Op:   fsnotify.Remove,
		Name: filepath.Join(base, "existing-subdir"),
	})
	eventsMustContain(t, events, fsnotify.Event{
		Op:   fsnotify.Rename,
		Name: filepath.Join(base, "newdir"),
	})
}

// TestTemplTempFiles tests the templ temp file scenario.
// When `templ fmt` is executed it creates a temporary formatted file
// then replaces the original file with the temp files.
// The watcher must ignore the temporary files in this scenario.
func TestTemplTempFiles(t *testing.T) {
	base, notifications := t.TempDir(), make(chan fsnotify.Event)
	w := runNewWatcher(t, base, notifications)

	require.NoError(t, w.Ignore("*.templ[0-9]*"))
	require.NoError(t, w.Add(base))
	ExpectWatched(t, w, []string{base})

	events := make([]fsnotify.Event, 3)

	// After every operation, wait for fsnotify to trigger,
	// otherwise events might get lost.

	MustCreateFile(t, base, "test.templ")
	events[0] = <-notifications

	// This file should be ignored.
	MustCreateFile(t, base, "test.templ123456")

	MustRemove(t, base, "test.templ")
	events[1] = <-notifications

	MustRename(t,
		filepath.Join(base, "test.templ123456"),
		filepath.Join(base, "test.templ"))
	events[2] = <-notifications

	// Event 0
	eventsMustContain(t, events, fsnotify.Event{
		Op:   fsnotify.Create,
		Name: filepath.Join(base, "test.templ"),
	})
	// Event 1
	eventsMustContain(t, events, fsnotify.Event{
		Op:   fsnotify.Remove,
		Name: filepath.Join(base, "test.templ"),
	})
	// Event 2
	eventsMustContain(t, events, fsnotify.Event{
		Op:   fsnotify.Create,
		Name: filepath.Join(base, "test.templ"),
	})
}

func eventsMustContain(t *testing.T, set []fsnotify.Event, contains fsnotify.Event) {
	t.Helper()
	for _, e := range set {
		// If Op is 0, match only by name; otherwise match both
		if contains.Op == 0 {
			if e.Name == contains.Name {
				return
			}
		} else if e.Op == contains.Op && e.Name == contains.Name {
			return
		}
	}
	t.Errorf("event set %#v doesn't contain event %#v", set, contains)
}

func TestWatcherRunCancelContext(t *testing.T) {
	base := t.TempDir()
	w, err := watcher.New(base, func(ctx context.Context, e fsnotify.Event) error {
		return nil
	})
	require.NoError(t, err)

	chErr := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { chErr <- w.Run(ctx) }()
	require.NoError(t, w.Add(base))
	w.WaitRunning()

	ExpectWatched(t, w, []string{base})

	cancel()
	require.ErrorIs(t, <-chErr, context.Canceled)

	require.ErrorIs(t, w.Add("new"), watcher.ErrClosed)
	require.ErrorIs(t, w.Remove("new"), watcher.ErrClosed)
	require.ErrorIs(t, w.Run(context.Background()), watcher.ErrClosed)
	require.ErrorIs(t, w.Ignore(".ignored"), watcher.ErrClosed)
	ExpectWatched(t, w, []string{})
}

func TestWatcherErrRunning(t *testing.T) {
	base := t.TempDir()
	w := runNewWatcher(t, base, nil)
	require.NoError(t, w.Add(base)) // Wait for the runner to start
	require.ErrorIs(t, w.Run(context.Background()), watcher.ErrRunning)
}

func TestWatcherAdd_AlreadyWatched(t *testing.T) {
	base := t.TempDir()
	w := runNewWatcher(t, base, nil)

	ExpectWatched(t, w, []string{})
	require.NoError(t, w.Add(base))
	ExpectWatched(t, w, []string{base})
	require.NoError(t, w.Add(base)) // Add again
	ExpectWatched(t, w, []string{base})
}

func TestWatcherRemove(t *testing.T) {
	base := t.TempDir()
	w := runNewWatcher(t, base, nil)

	MustMkdir(t, base, "sub")
	MustMkdir(t, base, "sub", "subsub")
	MustMkdir(t, base, "sub", "subsub2")
	MustMkdir(t, base, "sub", "subsub2", "subsubsub")
	MustMkdir(t, base, "sub2")

	ExpectWatched(t, w, []string{})

	require.NoError(t, w.Add(base))
	ExpectWatched(t, w, []string{
		base,
		filepath.Join(base, "sub"),
		filepath.Join(base, "sub", "subsub"),
		filepath.Join(base, "sub", "subsub2"),
		filepath.Join(base, "sub", "subsub2", "subsubsub"),
		filepath.Join(base, "sub2"),
	})

	require.NoError(t, w.Remove(filepath.Join(base, "sub", "subsub2", "subsubsub")))
	ExpectWatched(t, w, []string{
		base,
		filepath.Join(base, "sub"),
		filepath.Join(base, "sub", "subsub"),
		filepath.Join(base, "sub", "subsub2"),
		filepath.Join(base, "sub2"),
	})

	require.NoError(t, w.Remove(base))
	ExpectWatched(t, w, []string{})
}

func TestWatcherIgnore(t *testing.T) {
	base := t.TempDir()
	MustMkdir(t, base, ".hidden")

	var lock sync.Mutex
	var events []fsnotify.Event

	w, err := watcher.New(base, func(ctx context.Context, e fsnotify.Event) error {
		lock.Lock()
		events = append(events, e)
		lock.Unlock()
		return nil
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = w.Run(ctx) }()
	w.WaitRunning()

	require.NoError(t, w.Add(base))
	require.NoError(t, w.Add(filepath.Join(base, ".hidden")))
	ExpectWatched(t, w, []string{base, filepath.Join(base, ".hidden")})

	require.NoError(t, w.Ignore(".*"))
	ExpectWatched(t, w, []string{base})

	// These should be ignored
	MustCreateFile(t, base, ".ignore")
	MustMkdir(t, base, ".ignorenewdir")
	MustCreateFile(t, base, ".hidden", "ignored")

	// These should generate events
	MustCreateFile(t, base, "notignored")
	MustMkdir(t, base, "notignoreddir")

	// Give filesystem events time to propagate
	time.Sleep(100 * time.Millisecond)

	lock.Lock()
	eventsCopy := append([]fsnotify.Event{}, events...)
	lock.Unlock()

	// Verify we got events for non-ignored files
	eventsMustContain(t, eventsCopy, fsnotify.Event{
		Name: filepath.Join(base, "notignored"),
	})
	eventsMustContain(t, eventsCopy, fsnotify.Event{
		Name: filepath.Join(base, "notignoreddir"),
	})

	// Verify no events for ignored files
	for _, e := range eventsCopy {
		require.NotContains(t, e.Name, ".ignore")
		require.NotContains(t, e.Name, ".hidden")
	}

	ExpectWatched(t, w, []string{
		base,
		filepath.Join(base, "notignoreddir"),
	})
}

func TestWatcherUnignore(t *testing.T) {
	base, notifications := t.TempDir(), make(chan fsnotify.Event)
	w := runNewWatcher(t, base, notifications)

	require.NoError(t, w.Add(base))
	ExpectWatched(t, w, []string{base})

	{
		p := filepath.Join(base, ".*")
		require.NoError(t, w.Ignore(p))
		w.Unignore(p)
	}

	MustMkdir(t, base, ".hidden")
	require.Equal(t, fsnotify.Event{
		Op:   fsnotify.Create,
		Name: filepath.Join(base, ".hidden"),
	}, <-notifications)
	ExpectWatched(t, w, []string{base, filepath.Join(base, ".hidden")})
}

func ExpectWatched(t *testing.T, w *watcher.Watcher, expect []string) {
	t.Helper()
	actual := []string{}
	w.RangeWatchedDirs(func(path string) (continueIter bool) {
		actual = append(actual, path)
		return true
	})
	require.Len(t, actual, len(expect), "actual: %v", actual)
	for _, exp := range expect {
		require.Contains(t, actual, exp)
	}
}

func MustMkdir(t *testing.T, pathParts ...string) {
	t.Helper()
	err := os.Mkdir(filepath.Join(pathParts...), 0o777)
	require.NoError(t, err)
}

func MustCreateFile(t *testing.T, pathParts ...string) *os.File {
	t.Helper()
	f, err := os.Create(filepath.Join(pathParts...))
	require.NoError(t, err)
	return f
}

func MustRemove(t *testing.T, pathParts ...string) {
	t.Helper()
	err := os.Remove(filepath.Join(pathParts...))
	require.NoError(t, err)
}

func MustRename(t *testing.T, from, to string) {
	t.Helper()
	err := os.Rename(from, to)
	require.NoError(t, err)
}

// TestConcurrency requires go test -race
func TestConcurrency(t *testing.T) {
	base := t.TempDir()
	w := runNewWatcher(t, base, nil)

	var wg sync.WaitGroup
	wg.Add(4)
	go func() { defer wg.Done(); panicOnErr(w.Ignore(".ignored")) }()
	go func() { defer wg.Done(); w.Unignore(".ignored") }()
	go func() { defer wg.Done(); panicOnErr(w.Add(base)) }()
	go func() { defer wg.Done(); panicOnErr(w.Remove(base)) }()
	wg.Wait()
}

func runNewWatcher(
	t *testing.T, baseDir string, notify chan<- fsnotify.Event,
) *watcher.Watcher {
	t.Helper()
	w, err := watcher.New(baseDir, func(ctx context.Context, e fsnotify.Event) error {
		if notify != nil {
			notify <- e
		}
		return nil
	})
	require.NoError(t, err)

	var wg sync.WaitGroup
	wg.Add(1)
	t.Cleanup(func() {
		require.NoError(t, w.Close())
		wg.Wait() // Wait until the runner stops
	})
	go func() {
		defer wg.Done()
		err := w.Run(context.Background())
		if err == nil || err == watcher.ErrClosed {
			return
		}
		panic(err)
	}()
	w.WaitRunning()
	return w
}

func panicOnErr(err error) {
	if err != nil {
		panic(err)
	}
}
