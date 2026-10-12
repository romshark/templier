package fswalk_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/romshark/templier/internal/fswalk"
	"github.com/alecthomas/assert/v2"
)

func Test(t *testing.T) {
	d := t.TempDir()
	f1_txt := filepath.Join(d, "f1.txt")
	f2_go := filepath.Join(d, "f2.go")
	subDir := filepath.Join(d, "subdir")
	subDir_f1_txt := filepath.Join(subDir, "f1.txt")
	subDir_f2_go := filepath.Join(subDir, "f2.go")
	subEmpty := filepath.Join(subDir, "empty")
	subSubDir := filepath.Join(subDir, "subsubdir")
	subSubDir_f1_txt := filepath.Join(subSubDir, "f1.txt")
	subSubDir_f2_go := filepath.Join(subSubDir, "f2.go")
	subSubSubEmpty := filepath.Join(subSubDir, "empty")

	WriteFile(t, f1_txt, "f1_txt")
	WriteFile(t, f2_go, "f2_go")
	WriteFile(t, subDir_f1_txt, "subDir_f1_txt")
	WriteFile(t, subDir_f2_go, "subDir_f2_go")
	WriteFile(t, subSubDir_f1_txt, "subSubDir_f1_txt")
	WriteFile(t, subSubDir_f2_go, "subSubDir_f2_go")

	assert.NoError(t, os.MkdirAll(subEmpty, 0o777))
	assert.NoError(t, os.MkdirAll(subSubSubEmpty, 0o777))

	t.Run("Files", func(t *testing.T) {
		f := func(t *testing.T, dir string, expect ...string) {
			t.Helper()
			actual := []string{}
			err := fswalk.Files(dir, func(name string) error {
				actual = append(actual, name)
				return nil
			})
			assert.NoError(t, err)
			assert.Equal(t, expect, actual)
		}

		f(t, d,
			f1_txt, f2_go,
			subDir_f1_txt, subDir_f2_go,
			subSubDir_f1_txt, subSubDir_f2_go)

		f(t, subDir,
			subDir_f1_txt, subDir_f2_go,
			subSubDir_f1_txt, subSubDir_f2_go)

		f(t, subSubDir,
			subSubDir_f1_txt, subSubDir_f2_go)
	})

	t.Run("Dirs", func(t *testing.T) {
		f := func(t *testing.T, dir string, expect ...string) {
			t.Helper()
			actual := []string{}
			err := fswalk.Dirs(dir, func(name string) error {
				actual = append(actual, name)
				return nil
			})
			assert.NoError(t, err)
			assert.Equal(t, expect, actual)
		}

		f(t, d,
			d, subDir, subEmpty, subSubDir, subSubSubEmpty)

		f(t, subDir,
			subDir, subEmpty, subSubDir, subSubSubEmpty)

		f(t, subSubDir,
			subSubDir, subSubSubEmpty)
	})
}

func TestFilesSkipsDirSymlink(t *testing.T) {
	d := t.TempDir()

	realFile := filepath.Join(d, "real.txt")
	realDir := filepath.Join(d, "realdir")
	realDir_file := filepath.Join(realDir, "inner.txt")
	WriteFile(t, realFile, "real")
	WriteFile(t, realDir_file, "inner")

	// Symlink to a directory: must be skipped, not opened as a file.
	dirLink := filepath.Join(d, "dirlink")
	assert.NoError(t, os.Symlink(realDir, dirLink))
	// Symlink to a file: must still be visited (current behavior).
	fileLink := filepath.Join(d, "filelink")
	assert.NoError(t, os.Symlink(realFile, fileLink))

	actual := []string{}
	err := fswalk.Files(d, func(name string) error {
		actual = append(actual, name)
		return nil
	})
	assert.NoError(t, err)
	expect := []string{realFile, realDir_file, fileLink}
	slices.Sort(expect)
	slices.Sort(actual)
	assert.Equal(t, expect, actual)
	assert.NotSliceContains(t, actual, dirLink)
}

func TestFilesFnErr(t *testing.T) {
	d := t.TempDir()

	WriteFile(t, filepath.Join(d, "file.txt"), "")
	WriteFile(t, filepath.Join(d, "subdir", "file.txt"), "")

	ErrTest := errors.New("test error")

	counter := 0
	err := fswalk.Files(d, func(name string) error {
		counter++
		return ErrTest
	})

	assert.IsError(t, err, ErrTest)
	assert.Equal(t, 1, counter)
}

func WriteFile(t *testing.T, path, data string) {
	t.Helper()
	err := os.MkdirAll(filepath.Dir(path), 0o777)
	assert.NoError(t, err)
	err = os.WriteFile(path, []byte(data), 0o600)
	assert.NoError(t, err)
}
