package watcher

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/fsnotify/fsnotify"
	"github.com/gobwas/glob"
)

// Watcher is a recursive file watcher.
type Watcher struct {
	lock        sync.Mutex
	baseDir     string
	watchedDirs map[string]struct{}
	exclude     map[string]glob.Glob
	onChange    func(ctx context.Context, e fsnotify.Event)
	watcher     *fsnotify.Watcher
	ignored     atomic.Value // chan<- fsnotify.Event
	closed      bool
}

// New creates a new file watcher that executes onChange for any
// remove/create/change/chmod filesystem event.
// onChange will receive the ctx that was passed to Run.
// baseDir is used as the base path for relative ignore expressions.
func New(
	baseDir string,
	onChange func(ctx context.Context, e fsnotify.Event),
) (*Watcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &Watcher{
		baseDir:     baseDir,
		watchedDirs: make(map[string]struct{}),
		exclude:     make(map[string]glob.Glob),
		onChange:    onChange,
		watcher:     watcher,
	}, nil
}

var ErrClosed = errors.New("closed")

// RangeWatchedDirs calls fn for every currently watched directory.
// Noop if the watcher is closed.
func (w *Watcher) RangeWatchedDirs(fn func(path string) (continueIter bool)) {
	w.lock.Lock()
	defer w.lock.Unlock()
	if w.closed {
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
	defer w.lock.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	return w.watcher.Close()
}

// Run runs the watcher.
// Returns ErrClosed if already closed.
func (w *Watcher) Run(ctx context.Context) error {
	w.lock.Lock()
	if w.closed {
		w.lock.Unlock()
		return ErrClosed
	}
	w.lock.Unlock()

	defer w.Close()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err() // Watching canceled
		case e := <-w.watcher.Events:
			switch e.Op {
			case fsnotify.Create, fsnotify.Remove, fsnotify.Rename:
				if w.isDirEvent(e) {
					switch e.Op {
					case fsnotify.Create:
						// New sub-directory was created, start watching it.
						if err := w.Add(e.Name); err != nil {
							return fmt.Errorf("adding created directory: %w", err)
						}
					case fsnotify.Remove, fsnotify.Rename:
						// Sub-directory was removed or renamed, stop watching it.
						// A new create notification will readd it.
						if err := w.Remove(e.Name); err != nil {
							return fmt.Errorf("removing directory: %w", err)
						}
					}
				}
			case 0: // Unknown event type
				continue
			}
			if err := w.isExluded(e.Name); err != nil {
				if err == errExcluded {
					if c := w.ignored.Load(); c != nil {
						c.(chan<- fsnotify.Event) <- e
					}
					continue // Object is excluded from watcher, don't notify
				}
				return fmt.Errorf("isExluded: %w", err)
			}
			w.onChange(ctx, e)
		case err := <-w.watcher.Errors:
			if err != nil {
				return fmt.Errorf("watching: %w", err)
			}
		}
	}
}

// SetIgnoredChan should only be used for testing purposes,
// it sets the channel ignored fsnotify events are written to.
func (w *Watcher) SetIgnoredChan(c chan<- fsnotify.Event) { w.ignored.Store(c) }

// Ignore adds an ignore glob filter and removes all currently
// watched directories that match the expression.
// Returns ErrClosed if the watcher is closed.
func (w *Watcher) Ignore(globExpression string) error {
	g, err := glob.Compile(globExpression)
	if err != nil {
		return err
	}

	w.lock.Lock()
	defer w.lock.Unlock()

	if w.closed {
		return ErrClosed
	}

	w.exclude[globExpression] = g
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
// Noop if filter doesn't exist or the watcher is closed.
func (w *Watcher) Unignore(globExpression string) {
	w.lock.Lock()
	defer w.lock.Unlock()
	if w.closed {
		return
	}
	delete(w.exclude, globExpression)
}

// Add starts watching the directory and all of its subdirectories recursively.
// Returns ErrClosed if the watcher is already closed.
func (w *Watcher) Add(dir string) error {
	w.lock.Lock()
	defer w.lock.Unlock()
	if w.closed {
		return ErrClosed
	}
	err := forEachDir(dir, func(dir string) error {
		if err := w.isExluded(dir); err != nil {
			if err == errExcluded {
				return nil // Directory is exluded from watching.
			}
		}
		if _, ok := w.watchedDirs[dir]; ok {
			return errStopTraversal // Directory already watched.
		}
		w.watchedDirs[dir] = struct{}{}
		return w.watcher.Add(dir)
	})
	if err == errStopTraversal {
		return nil
	}
	return err
}

var errStopTraversal = errors.New("stop recursive traversal")

// Remove stops watching the directory and all of its subdirectories recursively.
// Returns ErrClosed if the watcher is already closed.
func (w *Watcher) Remove(dir string) error {
	w.lock.Lock()
	defer w.lock.Unlock()
	if w.closed {
		return ErrClosed
	}
	return w.remove(dir)
}

func (w *Watcher) remove(dir string) error {
	if _, ok := w.watchedDirs[dir]; !ok {
		return nil
	}
	delete(w.watchedDirs, dir)
	if err := w.removeWatcher(dir); err != nil {
		return err
	}

	// Stop all sub-directory watchers
	for p := range w.watchedDirs {
		if strings.HasPrefix(p, dir) {
			delete(w.watchedDirs, p)
			if err := w.removeWatcher(dir); err != nil {
				return err
			}
		}
	}
	return nil
}

// removeWatcher ignores ErrNonExistentWatch when removing a watcher.
func (w *Watcher) removeWatcher(dir string) error {
	if err := w.watcher.Remove(dir); err != nil {
		if !errors.Is(err, fsnotify.ErrNonExistentWatch) {
			return err
		}
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
	for _, x := range w.exclude {
		if x.Match(relPath) {
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

// forEachDir executes fn for every subdirectory of pathDir,
// including pathDir itself, recursively.
func forEachDir(pathDir string, fn func(dir string) error) error {
	// Use filepath.Walk to traverse directories
	err := filepath.Walk(pathDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err // Stop walking the directory tree.
		}
		if !info.IsDir() {
			return nil // Continue walking.
		}
		if err = fn(path); err != nil {
			return err
		}
		return nil
	})
	return err
}
