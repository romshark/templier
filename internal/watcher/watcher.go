// Package watcher provides a recursive file watcher implementation
// with support for glob expression based exclusions and event deduplication
// based on xxhash file checksums.
package watcher

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"log/slog"

	"github.com/romshark/templier/internal/filereg"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/fsnotify/fsnotify"
)

// Watcher is a recursive file watcher.
type Watcher struct {
	lock         sync.Mutex
	runnerStart  sync.WaitGroup
	closeOnce    sync.Once
	baseDir      string
	logger       *slog.Logger
	fileRegistry *filereg.Registry
	watchedDirs  map[string]struct{}
	exclude      map[string]string
	onChange     func(ctx context.Context, e fsnotify.Event) error
	watcher      *fsnotify.Watcher
	close        chan struct{}
	state        state
}

type state int8

const (
	_ state = iota
	stateClosed
	stateRunning
)

// eventQueueSize is the capacity of the internal event queue that decouples
// the fsnotify event loop from event handling.
const eventQueueSize = 4096

// New creates a new file watcher that executes onChange for any
// remove/create/change/chmod filesystem event.
// onChange will receive the ctx that was passed to Run.
// baseDir is used as the base path for relative ignore expressions.
func New(
	baseDir string,
	logger *slog.Logger,
	onChange func(ctx context.Context, e fsnotify.Event) error,
) (*Watcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{
		fileRegistry: filereg.New(),
		baseDir:      baseDir,
		logger:       logger,
		watchedDirs:  make(map[string]struct{}),
		exclude:      make(map[string]string),
		onChange:     onChange,
		watcher:      watcher,
		close:        make(chan struct{}),
	}
	w.runnerStart.Add(1)
	return w, nil
}

var (
	ErrClosed  = errors.New("closed")
	ErrRunning = errors.New("watcher is already running")
)

func (w *Watcher) signalClose() {
	w.closeOnce.Do(func() { close(w.close) })
}

// WaitRunning blocks and returns once the watcher is running.
// Returns immediately if the watcher is closed or running.
func (w *Watcher) WaitRunning() {
	w.lock.Lock()
	state := w.state
	w.lock.Unlock()

	if state == stateClosed {
		return
	}

	w.runnerStart.Wait()
}

// RangeWatchedDirs calls fn for every currently watched directory.
// Noop if the watcher is closed.
func (w *Watcher) RangeWatchedDirs(fn func(path string) (continueIter bool)) {
	w.lock.Lock()
	defer w.lock.Unlock()
	if w.state == stateClosed {
		return
	}
	for p := range w.watchedDirs {
		if !fn(p) {
			return
		}
	}
}

// Close stops watching everything and closes the watcher.
// Noop if the watcher is closed.
func (w *Watcher) Close() error {
	w.lock.Lock()
	if w.state == stateClosed {
		w.lock.Unlock()
		return nil
	}
	w.state = stateClosed
	w.signalClose()
	w.lock.Unlock()

	// The fsnotify watcher is closed without holding the lock because closing
	// it waits for its internal event loop to finish, which requires Run to
	// keep consuming events, which in turn requires the lock.
	return w.watcher.Close()
}

// Run runs the watcher. Can only be called once.
// Returns ErrClosed if closed, or ErrRunning if already running.
func (w *Watcher) Run(ctx context.Context) (err error) {
	w.lock.Lock()
	switch w.state {
	case stateClosed:
		w.lock.Unlock()
		return ErrClosed
	case stateRunning:
		w.lock.Unlock()
		return ErrRunning
	}

	func() { // Register all files from the base dir
		defer w.lock.Unlock()
		err = w.walk(w.baseDir, func(name string, d fs.DirEntry) error {
			if d.IsDir() {
				return nil
			}
			if d.Type()&fs.ModeSymlink != 0 {
				// Symlinked directories aren't walked, don't read them as files.
				if info, err := os.Stat(name); err == nil && info.IsDir() {
					return nil
				}
			}
			if _, err := w.fileRegistry.Add(name); err != nil {
				// A file that vanished or is temporarily inaccessible while
				// the tree is being walked must not abort the registration.
				w.logger.Debug("registering file", "name", name, "err", err)
			}
			return nil
		})

		// Signal runner readiness
		w.state = stateRunning
		w.runnerStart.Done()
	}()
	if err != nil {
		return fmt.Errorf("registering files from base dir: %w", err)
	}

	// fsnotify events are drained into an internal queue so that the fsnotify
	// event loop is never blocked by event handling. On Windows the fsnotify
	// backend serves watch registration and removal requests from the very
	// same goroutine that delivers events, hence handling an event while
	// holding the lock would deadlock any concurrent Add, Remove or Close.
	events := make(chan fsnotify.Event, eventQueueSize)
	go func() {
		for {
			select {
			case <-w.close:
				return
			case e, ok := <-w.watcher.Events:
				if !ok {
					return // The fsnotify watcher was closed.
				}
				select {
				case events <- e:
				case <-w.close:
					return
				}
			}
		}
	}()

	defer func() { _ = w.Close() }()
	for {
		select {
		case <-w.close:
			return ErrClosed // Close called
		case <-ctx.Done():
			w.lock.Lock()
			w.state = stateClosed
			w.signalClose()
			w.lock.Unlock()
			return ctx.Err() // Watching canceled
		case e := <-events:
			if e.Name == "" || e.Op == 0 {
				continue
			}
			if err := w.handleEvent(ctx, e); err != nil {
				return err
			}
		case err := <-w.watcher.Errors:
			if err != nil {
				return fmt.Errorf("watching: %w", err)
			}
		}
	}
}

func (w *Watcher) handleEvent(ctx context.Context, e fsnotify.Event) error {
	// Do internal state updates under the lock.
	w.lock.Lock()

	if err := w.isExluded(e.Name); err != nil {
		w.lock.Unlock()
		if err == errExcluded {
			return nil // Object is excluded from watcher, don't notify
		}
		return fmt.Errorf("isExluded: %w", err)
	}

	if w.isDirEvent(e) {
		switch e.Op {
		case fsnotify.Create:
			// New sub-directory was created, start watching it.
			if err := w.add(e.Name); err != nil {
				w.lock.Unlock()
				// Tools often create and delete temporary directories so quickly
				// that the directory is already gone when the event arrives.
				if w.skippable(e.Name, err) {
					return nil
				}
				return fmt.Errorf("adding created directory: %w", err)
			}
		case fsnotify.Remove, fsnotify.Rename:
			// Sub-directory was removed or renamed, stop watching it.
			// A new create notification will readd it.
			if err := w.remove(e.Name); err != nil {
				w.lock.Unlock()
				return fmt.Errorf("removing directory: %w", err)
			}
		}
	} else if e.Op == fsnotify.Write || e.Op == fsnotify.Create {
		updated, err := w.fileRegistry.Add(e.Name)
		if err != nil {
			// The file couldn't be checksummed because it's already gone
			// again or is temporarily inaccessible. This is normal for
			// editors and code generators that write to a temporary file and
			// rename or remove it right after. Windows reports a file that's
			// pending deletion as inaccessible rather than as non-existent.
			//
			// Since it's unknown whether anything changed at all the event is dropped.
			// Any actual change is followed by another event once the file becomes
			// accessible again or is removed. The watcher must never fail because of it.
			w.logger.Debug("ignoring event for inaccessible file",
				"name", e.Name, "err", err)
			w.fileRegistry.Remove(e.Name)
			w.lock.Unlock()
			return nil
		}
		if !updated { // File checksum hasn't changed, ignore event.
			w.lock.Unlock()
			return nil
		}
	} else if e.Op == fsnotify.Rename || e.Op == fsnotify.Remove {
		w.fileRegistry.Remove(e.Name)
	}

	onChange := w.onChange
	w.lock.Unlock()

	// Callback MUST be outside the lock (can block).
	return onChange(ctx, e)
}

// Ignore adds an ignore glob filter and removes all currently
// watched directories that match the expression.
// Returns ErrClosed if the watcher is already closed or not running.
func (w *Watcher) Ignore(globExpression string) error {
	if !doublestar.ValidatePattern(globExpression) {
		return fmt.Errorf("invalid glob pattern: %q", globExpression)
	}

	w.lock.Lock()
	defer w.lock.Unlock()

	if w.state == stateClosed {
		return ErrClosed
	}

	// Reset the file registry because we don't know what files will be excluded.
	w.fileRegistry.Reset()

	w.exclude[globExpression] = globExpression
	for dir := range w.watchedDirs {
		if dir == w.baseDir {
			continue
		}
		err := w.isExluded(dir)
		if err == errExcluded {
			if err := w.remove(dir); err != nil {
				return fmt.Errorf("removing %q: %w", dir, err)
			}
		} else if err != nil {
			return fmt.Errorf("checking exclusion for %q: %w", dir, err)
		}
	}
	return nil
}

// Unignore removes an ignore glob filter.
// Noop if filter doesn't exist or the watcher is closed or not running.
func (w *Watcher) Unignore(globExpression string) {
	w.lock.Lock()
	defer w.lock.Unlock()
	if w.state == stateClosed {
		return
	}
	delete(w.exclude, globExpression)
}

// Add starts watching the directory and all of its subdirectories recursively.
// Returns ErrClosed if the watcher is already closed or not running.
func (w *Watcher) Add(dir string) error {
	w.lock.Lock()
	defer w.lock.Unlock()
	if w.state == stateClosed {
		return ErrClosed
	}
	return w.add(dir)
}

func (w *Watcher) add(dir string) error {
	w.logger.Debug("watching directory", "dir", dir)
	return w.walk(dir, func(dir string, d fs.DirEntry) error {
		if !d.IsDir() {
			return nil
		}
		if _, ok := w.watchedDirs[dir]; ok {
			return filepath.SkipDir // Already watched.
		}
		if err := w.watchDir(dir); err != nil {
			return err
		}
		w.watchedDirs[dir] = struct{}{}
		return nil
	})
}

// watchDir starts watching dir.
func (w *Watcher) watchDir(dir string) error {
	err := w.watcher.Add(dir)
	if errors.Is(err, fs.ErrNotExist) {
		if _, statErr := os.Lstat(dir); statErr == nil {
			// With kqueue (macOS, BSD), fsnotify also fails when an entry of dir
			// vanishes while dir is being added. dir is watched nonetheless,
			// and the removal makes fsnotify watch the remaining entries.
			w.logger.Debug("entry vanished while adding directory",
				"dir", dir, "err", err)
			return nil
		}
	}
	return err
}

// walk calls fn for root and everything below it that isn't excluded, without entering
// excluded directories. Paths below root that vanish or can't be read are skipped.
func (w *Watcher) walk(root string, fn func(path string, d fs.DirEntry) error) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil {
			err = w.isExluded(path)
			if err == nil {
				err = fn(path, d)
			}
		}
		switch {
		case err == nil:
			return nil
		case err == errExcluded, path != root && w.skippable(path, err):
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		return err
	})
}

// skippable reports whether err means that path vanished or can't be read,
// which mustn't stop the watcher, and logs it if so.
func (w *Watcher) skippable(path string, err error) bool {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		w.logger.Debug("skipping removed path", "path", path, "err", err)
	case errors.Is(err, fs.ErrPermission):
		w.logger.Warn("skipping unreadable path", "path", path, "err", err)
	default:
		return false
	}
	return true
}

// Remove stops watching the directory and all of its subdirectories recursively.
// Returns ErrClosed if the watcher is already closed or not running.
func (w *Watcher) Remove(dir string) error {
	w.lock.Lock()
	defer w.lock.Unlock()
	if w.state == stateClosed {
		return ErrClosed
	}
	return w.remove(dir)
}

func (w *Watcher) remove(dir string) error {
	if _, ok := w.watchedDirs[dir]; !ok {
		return nil
	}
	w.logger.Debug("unwatch directory", "dir", dir)
	delete(w.watchedDirs, dir)
	if err := w.removeWatcher(dir); err != nil {
		return err
	}

	// Stop all sub-directory watchers
	prefix := dir + string(os.PathSeparator)
	for p := range w.watchedDirs {
		if strings.HasPrefix(p, prefix) {
			delete(w.watchedDirs, p)
			if err := w.removeWatcher(p); err != nil {
				return err
			}
		}
	}

	// Remove all files from the registry
	w.fileRegistry.RemoveWithPrefix(dir)

	return nil
}

// removeWatcher ignores ErrNonExistentWatch when removing a watcher.
func (w *Watcher) removeWatcher(dir string) error {
	if err := w.watcher.Remove(dir); err != nil {
		if errors.Is(err, fsnotify.ErrNonExistentWatch) {
			return nil
		}
		// A directory that no longer exists doesn't need to be unwatched.
		// Windows reports this instead of ErrNonExistentWatch because it
		// resolves the path before looking the watch up.
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	return nil
}

// isExluded returns errExcluded if path is excluded, otherwise returns nil.
func (w *Watcher) isExluded(path string) error {
	relPath, err := filepath.Rel(w.baseDir, path)
	if err != nil {
		return fmt.Errorf("determining relative path (base: %q; path: %q): %w",
			w.baseDir, path, err)
	}
	if relPath == "." {
		// The working directory shall never be excluded based on globs.
		return nil
	}
	relPath = filepath.ToSlash(relPath)
	for _, pattern := range w.exclude {
		if matched, _ := doublestar.Match(pattern, relPath); matched {
			return errExcluded
		}
	}
	return nil
}

var errExcluded = errors.New("path excluded")

func (w *Watcher) isDirEvent(e fsnotify.Event) bool {
	switch e.Op {
	case fsnotify.Create, fsnotify.Write, fsnotify.Chmod:
		fileInfo, err := os.Stat(e.Name)
		if err != nil {
			return false
		}
		return fileInfo.IsDir()
	}
	_, ok := w.watchedDirs[e.Name]
	return ok
}
