package watcher_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/romshark/templier/internal/watcher"

	"github.com/alecthomas/assert/v2"
	"github.com/fsnotify/fsnotify"
)

func TestWatcher(t *testing.T) {
	base, notifications := t.TempDir(), make(chan fsnotify.Event, eventBufferSize)
	w := runNewWatcher(t, base, notifications)

	// Create a sub-directory that exists even before Run
	MustMkdir(t, base, "existing-subdir")

	assert.NoError(t, w.Add(base))

	ExpectWatched(t, w, []string{
		base,
		filepath.Join(base, "existing-subdir"),
	})

	var events []fsnotify.Event

	// After every operation, wait for the expected event,
	// otherwise the watcher state is checked before it was updated.
	MustCreateFile(t, base, "newfile")
	awaitEvent(t, notifications, &events, fsnotify.Event{
		Op:   fsnotify.Create,
		Name: filepath.Join(base, "newfile"),
	})

	MustMkdir(t, base, "newdir")
	awaitEvent(t, notifications, &events, fsnotify.Event{
		Op:   fsnotify.Create,
		Name: filepath.Join(base, "newdir"),
	})
	ExpectWatched(t, w, []string{
		base,
		filepath.Join(base, "existing-subdir"),
		filepath.Join(base, "newdir"),
	})

	MustMkdir(t, base, "newdir", "subdir")
	awaitEvent(t, notifications, &events, fsnotify.Event{
		Op:   fsnotify.Create,
		Name: filepath.Join(base, "newdir", "subdir"),
	})
	ExpectWatched(t, w, []string{
		base,
		filepath.Join(base, "existing-subdir"),
		filepath.Join(base, "newdir"),
		filepath.Join(base, "newdir", "subdir"),
	})

	MustCreateFile(t, base, "newdir", "subdir", "subfile")
	awaitEvent(t, notifications, &events, fsnotify.Event{
		Op:   fsnotify.Create,
		Name: filepath.Join(base, "newdir", "subdir", "subfile"),
	})

	MustCreateFile(t, base, "newdir", "subdir", "subfile2")
	awaitEvent(t, notifications, &events, fsnotify.Event{
		Op:   fsnotify.Create,
		Name: filepath.Join(base, "newdir", "subdir", "subfile2"),
	})

	MustCreateFile(t, base, "existing-subdir", "subfile3")
	awaitEvent(t, notifications, &events, fsnotify.Event{
		Op:   fsnotify.Create,
		Name: filepath.Join(base, "existing-subdir", "subfile3"),
	})

	MustRemove(t, base, "existing-subdir", "subfile3")
	awaitEvent(t, notifications, &events, fsnotify.Event{
		Op:   fsnotify.Remove,
		Name: filepath.Join(base, "existing-subdir", "subfile3"),
	})

	MustRemove(t, base, "existing-subdir")
	awaitEvent(t, notifications, &events, fsnotify.Event{
		Op:   fsnotify.Remove,
		Name: filepath.Join(base, "existing-subdir"),
	})
}

// TestWatcherRenameDir tests renaming a watched directory that
// contains a watched sub-directory.
func TestWatcherRenameDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows refuses to rename a directory while a handle to any of its
		// sub-directories is open, which a recursive watcher keeps open for any
		// directory that has them. This is a limitation of the platform,
		// the watcher can't do anything about it.
		t.Skip("Windows can't rename a directory " +
			"while its sub-directories are being watched")
	}

	base, notifications := t.TempDir(), make(chan fsnotify.Event, eventBufferSize)
	w := runNewWatcher(t, base, notifications)

	MustMkdir(t, base, "newdir")
	MustMkdir(t, base, "newdir", "subdir")

	assert.NoError(t, w.Add(base))
	ExpectWatched(t, w, []string{
		base,
		filepath.Join(base, "newdir"),
		filepath.Join(base, "newdir", "subdir"),
	})

	var events []fsnotify.Event

	// Renaming generates two events, the rename of the original directory and
	// the creation of the new one. The order they arrive in is platform dependent,
	// hence both are awaited without assuming any order.
	MustRename(t, filepath.Join(base, "newdir"), filepath.Join(base, "newname"))
	awaitEvents(t, notifications, &events,
		fsnotify.Event{
			Op:   fsnotify.Rename,
			Name: filepath.Join(base, "newdir"),
		},
		fsnotify.Event{
			Op:   fsnotify.Create,
			Name: filepath.Join(base, "newname"),
		},
	)
	ExpectWatched(t, w, []string{
		base,
		filepath.Join(base, "newname"),
		filepath.Join(base, "newname", "subdir"),
	})
}

// TestTemplTempFiles tests the templ temp file scenario.
// When `templ fmt` is executed it creates a temporary formatted file
// then replaces the original file with the temp files.
// The watcher must ignore the temporary files in this scenario.
func TestTemplTempFiles(t *testing.T) {
	base, notifications := t.TempDir(), make(chan fsnotify.Event, eventBufferSize)
	w := runNewWatcher(t, base, notifications)

	assert.NoError(t, w.Ignore("*.templ[0-9]*"))
	assert.NoError(t, w.Add(base))
	ExpectWatched(t, w, []string{base})

	var events []fsnotify.Event

	MustCreateFile(t, base, "test.templ")
	awaitEvent(t, notifications, &events, fsnotify.Event{
		Op:   fsnotify.Create,
		Name: filepath.Join(base, "test.templ"),
	})

	// This file should be ignored.
	MustCreateFile(t, base, "test.templ123456")

	MustRemove(t, base, "test.templ")
	awaitEvent(t, notifications, &events, fsnotify.Event{
		Op:   fsnotify.Remove,
		Name: filepath.Join(base, "test.templ"),
	})

	MustRename(t,
		filepath.Join(base, "test.templ123456"),
		filepath.Join(base, "test.templ"))
	awaitEvent(t, notifications, &events, fsnotify.Event{
		Op:   fsnotify.Create,
		Name: filepath.Join(base, "test.templ"),
	})

	// The ignored temp file must never have been reported.
	for _, e := range events {
		assert.NotEqual(t, filepath.Join(base, "test.templ123456"), e.Name)
	}
}

const (
	// awaitEventTimeout defines how long awaitEvent waits for an expected event.
	awaitEventTimeout = 10 * time.Second

	// eventBufferSize must be large enough for the notification channel to
	// never block the watcher, otherwise a test that stops reading,
	// because it failed or finished, would keep the watcher from shutting down.
	eventBufferSize = 256
)

// awaitEvent blocks until the expected event was received appending every
// event received in the meantime to received.
//
// The exact sequence of filesystem events is platform dependent.
// Windows for example reports operations that Linux and macOS don't report at all,
// hence tests must never assume an exact number of events in an exact order,
// they must wait for the events they're interested in instead.
func awaitEvent(
	t *testing.T,
	notifications <-chan fsnotify.Event,
	received *[]fsnotify.Event,
	expected fsnotify.Event,
) {
	t.Helper()
	timeout := time.After(awaitEventTimeout)
	for {
		select {
		case e := <-notifications:
			*received = append(*received, e)
			if e.Op == expected.Op && e.Name == expected.Name {
				return
			}
		case <-timeout:
			t.Fatalf("timed out waiting for event %#v; received: %#v",
				expected, *received)
			return
		}
	}
}

// awaitEvents blocks until every expected event was received, in any order,
// appending every event received in the meantime to received.
func awaitEvents(
	t *testing.T,
	notifications <-chan fsnotify.Event,
	received *[]fsnotify.Event,
	expected ...fsnotify.Event,
) {
	t.Helper()
	pending := make([]fsnotify.Event, len(expected))
	copy(pending, expected)

	// Expected events may already be among the ones received earlier.
	for _, e := range *received {
		pending = removeEvent(pending, e)
	}

	timeout := time.After(awaitEventTimeout)
	for len(pending) > 0 {
		select {
		case e := <-notifications:
			*received = append(*received, e)
			pending = removeEvent(pending, e)
		case <-timeout:
			t.Fatalf("timed out waiting for events %#v; received: %#v",
				pending, *received)
			return
		}
	}
}

// removeEvent removes the first event matching e from pending.
func removeEvent(pending []fsnotify.Event, e fsnotify.Event) []fsnotify.Event {
	for i, p := range pending {
		if p.Op == e.Op && p.Name == e.Name {
			return append(pending[:i], pending[i+1:]...)
		}
	}
	return pending
}

// awaitNames polls snapshot until every expected name was reported,
// storing the last snapshot it took in received.
func awaitNames(
	t *testing.T,
	received *[]fsnotify.Event,
	snapshot func() []fsnotify.Event,
	expected ...string,
) {
	t.Helper()
	timeout := time.After(awaitEventTimeout)
	for {
		*received = snapshot()
		missing := false
		for _, name := range expected {
			found := false
			for _, e := range *received {
				if e.Name == name {
					found = true
					break
				}
			}
			if !found {
				missing = true
				break
			}
		}
		if !missing {
			return
		}
		select {
		case <-timeout:
			t.Fatalf("timed out waiting for events %#v; received: %#v",
				expected, *received)
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
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
	w, err := watcher.New(base, slog.Default(), func(ctx context.Context, e fsnotify.Event) error {
		return nil
	})
	assert.NoError(t, err)

	chErr := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { chErr <- w.Run(ctx) }()
	assert.NoError(t, w.Add(base))
	w.WaitRunning()

	ExpectWatched(t, w, []string{base})

	cancel()
	assert.IsError(t, <-chErr, context.Canceled)

	assert.IsError(t, w.Add("new"), watcher.ErrClosed)
	assert.IsError(t, w.Remove("new"), watcher.ErrClosed)
	assert.IsError(t, w.Run(context.Background()), watcher.ErrClosed)
	assert.IsError(t, w.Ignore(".ignored"), watcher.ErrClosed)
	ExpectWatched(t, w, []string{})
}

func TestWatcherErrRunning(t *testing.T) {
	base := t.TempDir()
	w := runNewWatcher(t, base, nil)
	assert.NoError(t, w.Add(base)) // Wait for the runner to start
	assert.IsError(t, w.Run(context.Background()), watcher.ErrRunning)
}

func TestWatcherAdd_AlreadyWatched(t *testing.T) {
	base := t.TempDir()
	w := runNewWatcher(t, base, nil)

	ExpectWatched(t, w, []string{})
	assert.NoError(t, w.Add(base))
	ExpectWatched(t, w, []string{base})
	assert.NoError(t, w.Add(base)) // Add again
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

	assert.NoError(t, w.Add(base))
	ExpectWatched(t, w, []string{
		base,
		filepath.Join(base, "sub"),
		filepath.Join(base, "sub", "subsub"),
		filepath.Join(base, "sub", "subsub2"),
		filepath.Join(base, "sub", "subsub2", "subsubsub"),
		filepath.Join(base, "sub2"),
	})

	assert.NoError(t, w.Remove(filepath.Join(base, "sub", "subsub2", "subsubsub")))
	ExpectWatched(t, w, []string{
		base,
		filepath.Join(base, "sub"),
		filepath.Join(base, "sub", "subsub"),
		filepath.Join(base, "sub", "subsub2"),
		filepath.Join(base, "sub2"),
	})

	assert.NoError(t, w.Remove(base))
	ExpectWatched(t, w, []string{})
}

func TestWatcherIgnore(t *testing.T) {
	base := t.TempDir()
	MustMkdir(t, base, ".hidden")

	var lock sync.Mutex
	var events []fsnotify.Event

	w, err := watcher.New(base, slog.Default(), func(ctx context.Context, e fsnotify.Event) error {
		lock.Lock()
		events = append(events, e)
		lock.Unlock()
		return nil
	})
	assert.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = w.Run(ctx) }()
	w.WaitRunning()

	assert.NoError(t, w.Add(base))
	assert.NoError(t, w.Add(filepath.Join(base, ".hidden")))
	ExpectWatched(t, w, []string{base, filepath.Join(base, ".hidden")})

	assert.NoError(t, w.Ignore(".*"))
	ExpectWatched(t, w, []string{base})

	// These should be ignored
	MustCreateFile(t, base, ".ignore")
	MustMkdir(t, base, ".ignorenewdir")
	MustCreateFile(t, base, ".hidden", "ignored")

	// These should generate events
	MustCreateFile(t, base, "notignored")
	MustMkdir(t, base, "notignoreddir")

	// The time filesystem events take to propagate is platform dependent,
	// hence they're awaited instead of being given a fixed grace period.
	var eventsCopy []fsnotify.Event
	awaitNames(t, &eventsCopy, func() []fsnotify.Event {
		lock.Lock()
		defer lock.Unlock()
		return append([]fsnotify.Event{}, events...)
	}, filepath.Join(base, "notignored"), filepath.Join(base, "notignoreddir"))

	// Verify we got events for non-ignored files
	eventsMustContain(t, eventsCopy, fsnotify.Event{
		Name: filepath.Join(base, "notignored"),
	})
	eventsMustContain(t, eventsCopy, fsnotify.Event{
		Name: filepath.Join(base, "notignoreddir"),
	})

	// Verify no events for ignored files
	for _, e := range eventsCopy {
		assert.NotContains(t, e.Name, ".ignore")
		assert.NotContains(t, e.Name, ".hidden")
	}

	ExpectWatched(t, w, []string{
		base,
		filepath.Join(base, "notignoreddir"),
	})
}

func TestWatcherUnignore(t *testing.T) {
	base, notifications := t.TempDir(), make(chan fsnotify.Event, eventBufferSize)
	w := runNewWatcher(t, base, notifications)

	assert.NoError(t, w.Add(base))
	ExpectWatched(t, w, []string{base})

	{
		p := filepath.Join(base, ".*")
		assert.NoError(t, w.Ignore(p))
		w.Unignore(p)
	}

	MustMkdir(t, base, ".hidden")
	assert.Equal(t, fsnotify.Event{
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
	assert.Equal(t, len(expect), len(actual), "actual: %v", actual)
	for _, exp := range expect {
		assert.SliceContains(t, actual, exp)
	}
}

func MustMkdir(t *testing.T, pathParts ...string) {
	t.Helper()
	err := os.Mkdir(filepath.Join(pathParts...), 0o777)
	assert.NoError(t, err)
}

// MustCreateFile creates a file and closes it immediately. The handle must not
// be kept open because Windows refuses to remove or rename files that are still in use,
// which the tests rely on being possible.
func MustCreateFile(t *testing.T, pathParts ...string) {
	t.Helper()
	f, err := os.Create(filepath.Join(pathParts...))
	assert.NoError(t, err)
	assert.NoError(t, f.Close())
}

func MustRemove(t *testing.T, pathParts ...string) {
	t.Helper()
	err := os.Remove(filepath.Join(pathParts...))
	assert.NoError(t, err)
}

func MustRename(t *testing.T, from, to string) {
	t.Helper()
	err := os.Rename(from, to)
	assert.NoError(t, err)
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
	w, err := watcher.New(baseDir, slog.Default(), func(ctx context.Context, e fsnotify.Event) error {
		if notify != nil {
			notify <- e
		}
		return nil
	})
	assert.NoError(t, err)

	var wg sync.WaitGroup
	wg.Add(1)
	t.Cleanup(func() {
		assert.NoError(t, w.Close())
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
