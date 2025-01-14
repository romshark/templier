package templgofilereg_test

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/romshark/templier/internal/templgofilereg"
	"github.com/stretchr/testify/require"
)

func overwriteFile(srcPath, destPath string) error {
	// Open the source file
	srcFile, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	// Open the destination file with truncation
	destFile, err := os.OpenFile(destPath, os.O_WRONLY|os.O_TRUNC|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer destFile.Close()

	// Copy the contents
	_, err = io.Copy(destFile, srcFile)
	return err
}

func TestComparer(t *testing.T) {
	t.Parallel()

	testFile := filepath.Join(t.TempDir(), "test_templ.go")

	const pathOriginal = "./testdata/original.gocode"
	const pathRefresh = "./testdata/refresh.gocode"
	const pathRecompile = "./testdata/recompile.gocode"

	c := templgofilereg.New()

	check := func(inputFilePath string, expectRecompile bool) {
		t.Helper()
		overwriteFile(inputFilePath, testFile)
		recompile, err := c.Compare(testFile)
		require.NoError(t, err)
		// Repeated check, no change relative to previous call.
		require.Equal(t, expectRecompile, recompile)
	}

	check(pathOriginal, true)

	// Repeated check, no change relative to previous call.
	check(pathOriginal, false)

	// Only the text arguments changed.
	// The Go code remains structurally identical.
	check(pathRefresh, false)

	// Repeated check, no change relative to previous call.
	check(pathRefresh, false)

	// This change changed the structure of the Go code, not just the text.
	check(pathRecompile, true)

	// Repeated check, no change relative to previous call.
	check(pathRecompile, false)

	c.Remove(testFile)
	c.Remove(testFile) // No-op.
	c.Remove("")       // No-op.

	// File was removed and is considered new again.
	check(pathRecompile, true)

	// Repeated check, no change relative to previous call.
	check(pathRecompile, false)
}

func TestComparerErr(t *testing.T) {
	t.Parallel()

	testFile := filepath.Join(t.TempDir(), "test_templ.go")
	err := os.WriteFile(testFile, []byte("invalid"), 0o600)
	require.NoError(t, err)

	c := templgofilereg.New()
	recompile, err := c.Compare(testFile)
	require.Zero(t, recompile)
	require.Error(t, err)
}
