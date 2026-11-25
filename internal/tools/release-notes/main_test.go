package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/alecthomas/assert/v2"
)

const changelog = `# Changelog

Intro.

## [Unreleased]

## [0.14.0] - 2026-12-02

### Added

- Newer entry.

## [0.13.6] - 2026-11-26

### Fixed

- Older entry.

[Unreleased]: https://github.com/romshark/templier/compare/v0.14.0...HEAD
[0.14.0]: https://github.com/romshark/templier/compare/v0.13.6...v0.14.0
[0.13.6]: https://github.com/romshark/templier/compare/v0.13.5...v0.13.6
`

func TestSection(t *testing.T) {
	for name, tc := range map[string]struct {
		version string
		want    string
		ok      bool
	}{
		"ends at next heading":    {version: "0.14.0", want: "### Added\n\n- Newer entry.\n", ok: true},
		"ends at link definition": {version: "0.13.6", want: "### Fixed\n\n- Older entry.\n", ok: true},
		"missing":                 {version: "0.12.0"},
		"prefix of a version":     {version: "0.1"},
		"empty":                   {version: "Unreleased"},
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := section(changelog, tc.version)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestLink(t *testing.T) {
	for name, tc := range map[string]struct {
		version string
		want    string
		ok      bool
	}{
		"latest": {
			version: "0.14.0",
			want:    "https://github.com/romshark/templier/compare/v0.13.6...v0.14.0",
			ok:      true,
		},
		"older": {
			version: "0.13.6",
			want:    "https://github.com/romshark/templier/compare/v0.13.5...v0.13.6",
			ok:      true,
		},
		"missing":             {version: "0.12.0"},
		"prefix of a version": {version: "0.1"},
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := link(changelog, tc.version)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestChangelogReleases tests that every released version
// has a valid heading, non-empty notes and a link definition.
func TestChangelogReleases(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "CHANGELOG.md"))
	assert.NoError(t, err)

	semver := regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)
	for line := range strings.Lines(string(b)) {
		rest, ok := strings.CutPrefix(line, "## [")
		if !ok {
			continue
		}
		version, _, ok := strings.Cut(rest, "]")
		assert.True(t, ok, "unterminated heading %q", line)
		if version == "Unreleased" {
			continue
		}
		assert.True(t, semver.MatchString(version), "heading %q", strings.TrimSpace(line))
		_, ok = section(string(b), version)
		assert.True(t, ok, "the section of %s has no notes", version)
		_, ok = link(string(b), version)
		assert.True(t, ok, "%s has no link definition", version)
	}
}
