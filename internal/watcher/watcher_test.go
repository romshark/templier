package watcher_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/romshark/templier/internal/watcher"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/require"
)

func TestWatcher(t *testing.T) {
	notifications := make(chan fsnotify.Event)
	base := t.TempDir()
	w, err := watcher.New(base, func(ctx context.Context, e fsnotify.Event) {
		notifications <- e
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, w.Close()) }()

	go func() { require.NoError(t, w.Run(context.Background())) }()

	// Create a sub-directory that exists even before Run
	MustMkdir(t, base, "existing-subdir")

	require.NoError(t, w.Add(base))

	ExpectWatched(t, w, []string{
		base,
		filepath.Join(base, "existing-subdir"),
	})

	events := make([]fsnotify.Event, 10)

	// After every operation, wait for fsnotify to trigger,
	// otherwise events might get lost.
	MustCreateFile(t, base, "newfile")
	events[0] = <-notifications

	MustMkdir(t, base, "newdir")
	events[1] = <-notifications
	ExpectWatched(t, w, []string{
		base,
		filepath.Join(base, "existing-subdir"),
		filepath.Join(base, "newdir"),
	})

	MustMkdir(t, base, "newdir", "subdir")
	events[2] = <-notifications
	ExpectWatched(t, w, []string{
		base,
		filepath.Join(base, "existing-subdir"),
		filepath.Join(base, "newdir"),
		filepath.Join(base, "newdir", "subdir"),
	})

	MustCreateFile(t, base, "newdir", "subdir", "subfile")
	events[3] = <-notifications

	MustCreateFile(t, base, "newdir", "subdir", "subfile2")
	events[4] = <-notifications

	MustCreateFile(t, base, "existing-subdir", "subfile3")
	events[5] = <-notifications

	MustRemove(t, base, "existing-subdir", "subfile3")
	events[6] = <-notifications

	MustRemove(t, base, "existing-subdir")
	events[7] = <-notifications

	// Renaming will generate two events, first the renaming event and later
	// the event of creation of a new directory.
	MustRename(t, filepath.Join(base, "newdir"), filepath.Join(base, "newname"))
	events[8] = <-notifications
	events[9] = <-notifications
	ExpectWatched(t, w, []string{
		base,
		filepath.Join(base, "newname"),
		filepath.Join(base, "newname/subdir"),
	})

	// Event 0
	require.Contains(t, events, fsnotify.Event{
		Op:   fsnotify.Create,
		Name: filepath.Join(base, "newfile"),
	})
	// Event 1
	require.Contains(t, events, fsnotify.Event{
		Op:   fsnotify.Create,
		Name: filepath.Join(base, "newdir"),
	})
	// Event 2
	require.Contains(t, events, fsnotify.Event{
		Op:   fsnotify.Create,
		Name: filepath.Join(base, "newdir", "subdir"),
	})
	// Event 3
	require.Contains(t, events, fsnotify.Event{
		Op:   fsnotify.Create,
		Name: filepath.Join(base, "newdir", "subdir", "subfile"),
	})
	// Event 4
	require.Contains(t, events, fsnotify.Event{
		Op:   fsnotify.Create,
		Name: filepath.Join(base, "newdir", "subdir", "subfile2"),
	})
	// Event 5
	require.Contains(t, events, fsnotify.Event{
		Op:   fsnotify.Create,
		Name: filepath.Join(base, "existing-subdir", "subfile3"),
	})
	// Event 6
	require.Contains(t, events, fsnotify.Event{
		Op:   fsnotify.Remove,
		Name: filepath.Join(base, "existing-subdir", "subfile3"),
	})
	// Event 7
	require.Contains(t, events, fsnotify.Event{
		Op:   fsnotify.Remove,
		Name: filepath.Join(base, "existing-subdir"),
	})
	// Event 8
	require.Contains(t, events, fsnotify.Event{
		Op:   fsnotify.Rename,
		Name: filepath.Join(base, "newdir"),
	})
	// Event 9
	require.Contains(t, events, fsnotify.Event{
		Op:   fsnotify.Create,
		Name: filepath.Join(base, "newname"),
	})
}

func TestWatcherClosed(t *testing.T) {
	base := t.TempDir()
	w, err := watcher.New(base, func(ctx context.Context, e fsnotify.Event) {})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		require.ErrorIs(t, w.Run(ctx), context.Canceled)
	}()
	require.NoError(t, w.Add(t.TempDir()))
	cancel()  // Close
	wg.Wait() // Wait for Run to cancel

	require.ErrorIs(t, w.Add("new"), watcher.ErrClosed)
	require.ErrorIs(t, w.Remove("new"), watcher.ErrClosed)
	require.ErrorIs(t, w.Run(context.Background()), watcher.ErrClosed)
	require.ErrorIs(t, w.Ignore(".ignored"), watcher.ErrClosed)

	ExpectWatched(t, w, []string{})
}

func TestWatcherAdd_AlreadyWatched(t *testing.T) {
	base := t.TempDir()
	w, err := watcher.New(base, func(ctx context.Context, e fsnotify.Event) {})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { require.ErrorIs(t, w.Run(ctx), context.Canceled) }()

	ExpectWatched(t, w, []string{})
	require.NoError(t, w.Add(base))
	ExpectWatched(t, w, []string{base})
	require.NoError(t, w.Add(base)) // Add again
	ExpectWatched(t, w, []string{base})
}

func TestWatcherRemove(t *testing.T) {
	base := t.TempDir()
	w, err := watcher.New(base, func(ctx context.Context, e fsnotify.Event) {})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { require.ErrorIs(t, w.Run(ctx), context.Canceled) }()

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

	notification := make(chan fsnotify.Event, 1)
	w, err := watcher.New(base, func(ctx context.Context, e fsnotify.Event) {
		notification <- e
	})
	require.NoError(t, err)

	ignored := make(chan fsnotify.Event)
	w.SetIgnoredChan(ignored)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { require.ErrorIs(t, w.Run(ctx), context.Canceled) }()

	require.NoError(t, w.Add(base))
	require.NoError(t, w.Add(filepath.Join(base, ".hidden")))

	ExpectWatched(t, w, []string{base, filepath.Join(base, ".hidden")})

	require.NoError(t, w.Ignore(".*"))
	// Expect .hidden watchers to be stopped
	ExpectWatched(t, w, []string{base})

	MustCreateFile(t, base, ".ignore")
	require.Equal(t, fsnotify.Event{
		Op:   fsnotify.Create,
		Name: filepath.Join(base, ".hidden"),
	}, <-ignored)
	require.Equal(t, fsnotify.Event{
		Op:   fsnotify.Create,
		Name: filepath.Join(base, ".ignore"),
	}, <-ignored)

	MustMkdir(t, base, ".ignore_new_dir")
	require.Equal(t, fsnotify.Event{
		Op:   fsnotify.Create,
		Name: filepath.Join(base, ".ignore_new_dir"),
	}, <-ignored)

	ExpectWatched(t, w, []string{base})
}

func TestWatcherUnignore(t *testing.T) {
	base := t.TempDir()
	notification := make(chan fsnotify.Event)
	w, err := watcher.New(base, func(ctx context.Context, e fsnotify.Event) {
		notification <- e
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { require.ErrorIs(t, w.Run(ctx), context.Canceled) }()

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
	}, <-notification)
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
