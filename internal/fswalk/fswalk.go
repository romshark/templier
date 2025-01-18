package fswalk

import (
	"os"
	"path/filepath"
)

// Files recursively iterates over all files in dir and applies fn to each file.
func Files(dir string, fn func(name string) error) error {
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		return fn(path)
	})
}

// Dirs recursively iterates over all directories in dir, including dir itself
// and applies fn to each.
func Dirs(dir string, fn func(name string) error) error {
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return nil
		}
		return fn(path)
	})
}
