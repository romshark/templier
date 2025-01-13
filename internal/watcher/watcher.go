// Package watcher provides a recursive file watcher implementation
// with support for glob expression based exclusions and event deduplication
// based on xxhash file checksums.
package watcher

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/romshark/templier/internal/filereg"
	"github.com/romshark/templier/internal/log"

	"github.com/fsnotify/fsnotify"
	"github.com/gobwas/glob"
)

// Watcher is a recursive file watcher.
type Watcher struct {
	lock         sync.Mutex
	runnerStart  sync.WaitGroup
	baseDir      string
	fileRegistry *filereg.Registry
	watchedDirs  map[string]struct{}
	exclude      map[string]glob.Glob
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

// New creates a new file watcher that executes onChange for any
// remove/create/change/chmod filesystem event.
// onChange will receive the ctx that was passed to Run.
// baseDir is used as the base path for relative ignore expressions.
func New(
	baseDir string,
	onChange func(ctx context.Context, e fsnotify.Event) error,
) (*Watcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{
		fileRegistry: filereg.New(),
		baseDir:      baseDir,
		watchedDirs:  make(map[string]struct{}),
		exclude:      make(map[string]glob.Glob),
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
	defer w.lock.Unlock()
	if w.state != stateRunning || w.state == stateClosed {
		return nil
	}
	close(w.close)
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
		err = filepath.Walk(w.baseDir, func(p string, i os.FileInfo, err error) error {
			if err != nil || i.IsDir() {
				return err
			}
			if err := w.isExluded(p); err != nil {
				if err == errExcluded {
					return nil // Object is excluded from watcher, don't notify
				}
				return fmt.Errorf("isExluded: %w", err)
			}
			_, err = w.fileRegistry.Add(p)
			return err
		})

		// Signal runner readiness
		w.state = stateRunning
		w.runnerStart.Done()
	}()
	if err != nil {
		return fmt.Errorf("registering files from base dir: %w", err)
	}

	defer w.Close()
	for {
		select {
		case <-w.close:
			w.lock.Lock()
			w.state = stateClosed
			w.lock.Unlock()
			return ErrClosed // Close called
		case <-ctx.Done():
			w.lock.Lock()
			close(w.close)
			w.state = stateClosed
			w.lock.Unlock()
			return ctx.Err() // Watching canceled
		case e := <-w.watcher.Events:
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
	w.lock.Lock()
	defer w.lock.Unlock()

	if err := w.isExluded(e.Name); err != nil {
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
				return fmt.Errorf("adding created directory: %w", err)
			}
		case fsnotify.Remove, fsnotify.Rename:
			// Sub-directory was removed or renamed, stop watching it.
			// A new create notification will readd it.
			if err := w.remove(e.Name); err != nil {
				return fmt.Errorf("removing directory: %w", err)
			}
		}
	} else if e.Op == fsnotify.Write || e.Op == fsnotify.Create {
		// A file was created.
		updated, err := w.fileRegistry.Add(e.Name)
		if err != nil {
			// Ignore not exist errors since those are usually triggered
			// by tools creating and deleting temporary files so quickly that
			// the watcher sees a file change but isn't fast enough to read it.
			if os.IsNotExist(err) {
				log.Errorf("adding created file (%q) to registry: %v",
					e.Name, err)
				return nil
			}
			return fmt.Errorf("adding created file (%q) to registry: %w",
				e.Name, err)
		}
		if !updated { // File checksum hasn't changed, ignore event.
			return nil
		}
	} else if e.Op == fsnotify.Rename || e.Op == fsnotify.Remove {
		// A file was (re)moved.
		w.fileRegistry.Remove(e.Name)
	}
	return w.onChange(ctx, e)
}

// Ignore adds an ignore glob filter and removes all currently
// watched directories that match the expression.
// Returns ErrClosed if the watcher is already closed or not running.
func (w *Watcher) Ignore(globExpression string) error {
	g, err := glob.Compile(globExpression)
	if err != nil {
		return err
	}

	w.lock.Lock()
	defer w.lock.Unlock()

	if w.state == stateClosed {
		return ErrClosed
	}

	// Reset the file registry because we don't know what files will be excluded.
	w.fileRegistry.Reset()

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
	log.Debugf("watching directory: %q", dir)
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
	log.Debugf("unwatch directory: %q", dir)
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

	// Remove all files from the registry
	w.fileRegistry.RemoveWithPrefix(dir)

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
	if relPath == "." {
		// The working directory shall never be excluded based on globs.
		return nil
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
