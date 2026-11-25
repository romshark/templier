// Command release-notes prints a release tag's section of CHANGELOG.md
// followed by the compare link from the version's link definition.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	var tag, path string
	flag.StringVar(&tag, "tag", "", "Release tag, such as v0.10.1")
	flag.StringVar(&path, "changelog", "CHANGELOG.md", "Changelog path")
	flag.Parse()

	if tag == "" {
		fmt.Fprintln(os.Stderr, "missing required -tag")
		os.Exit(2)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read changelog: %v\n", err)
		os.Exit(1)
	}
	version := strings.TrimPrefix(tag, "v")
	notes, ok := section(string(b), version)
	if !ok {
		fmt.Fprintf(os.Stderr, "%s has no section for %s\n", path, version)
		os.Exit(1)
	}
	url, ok := link(string(b), version)
	if !ok {
		fmt.Fprintf(os.Stderr, "%s has no link definition for %s\n", path, version)
		os.Exit(1)
	}
	fmt.Printf("%s\n**Full Changelog**: %s\n", notes, url)
}

// section returns a version's non-empty Keep a Changelog body.
func section(changelog, version string) (string, bool) {
	heading := "## [" + version + "]"
	var body strings.Builder
	in := false
	for line := range strings.Lines(changelog) {
		if !in {
			in = strings.HasPrefix(line, heading)
			continue
		}
		if strings.HasPrefix(line, "## [") || isLinkDefinition(line) {
			break
		}
		body.WriteString(line)
	}
	notes := strings.TrimSpace(body.String())
	if notes == "" {
		return "", false
	}
	return notes + "\n", true
}

// link returns the URL of a version's link definition.
// GoReleaser's --release-notes replaces the body GitHub generates,
// which otherwise ends with this compare link.
func link(changelog, version string) (string, bool) {
	prefix := "[" + version + "]: "
	for line := range strings.Lines(changelog) {
		if url, ok := strings.CutPrefix(line, prefix); ok {
			url = strings.TrimSpace(url)
			return url, url != ""
		}
	}
	return "", false
}

func isLinkDefinition(line string) bool {
	return strings.HasPrefix(line, "[") && strings.Contains(line, "]: ")
}
