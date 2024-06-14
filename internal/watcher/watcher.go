package watcher

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// Watcher is a recursive file watcher.
type Watcher struct {
	lock        sync.Mutex
	closed      bool
	watchedDirs map[string]struct{} // dir path -> closer channel
	onChange    func(ctx context.Context, e fsnotify.Event)
	watcher     *fsnotify.Watcher
}

// New creates a new file watcher that executes onChange for any
// remove/create/change/chmod filesystem event.
// onChange will receive the ctx that was passed to Run.
func New(onChange func(ctx context.Context, e fsnotify.Event)) (*Watcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &Watcher{
		watchedDirs: make(map[string]struct{}),
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
			case 0:
				continue
			}
			w.onChange(ctx, e)
		case err := <-w.watcher.Errors:
			if err != nil {
				return fmt.Errorf("watching: %w", err)
			}
		}
	}
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
		if _, ok := w.watchedDirs[dir]; ok {
			return errAlreadyWatched // Directory already watched
		}
		w.watchedDirs[dir] = struct{}{}
		return w.watcher.Add(dir)
	})
	if err == errAlreadyWatched {
		return nil
	}
	return err
}

var errAlreadyWatched = errors.New("directory already watched")

// Remove stops watching the directory and all of its subdirectories recursively.
// Returns ErrClosed if the watcher is already closed.
func (w *Watcher) Remove(dir string) error {
	w.lock.Lock()
	defer w.lock.Unlock()
	if w.closed {
		return ErrClosed
	}

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
