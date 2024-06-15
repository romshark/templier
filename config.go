package main

import (
	"encoding"
	"flag"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/gobwas/glob"
	"github.com/romshark/yamagiconf"
)

var config Config

type Config struct {
	serverOutPath string // Initialized from os.Getwd and os.TempDir

	App struct {
		// DirSrcRoot is the source root directory for the application server.
		DirSrcRoot string `yaml:"dir-src-root" validate:"dirpath,required"`

		dirSrcRootAbsolute string // Initialized from DirSrcRoot

		// Exclude defines glob expressions to match files exluded from watching.
		Exclude ExludeFiles `yaml:"exclude"`

		// DirCmd is the server cmd directory containing the `main` function.
		DirCmd string `yaml:"dir-cmd" validate:"dirpath,required"`

		// DirWork is the working directory to run the application server from.
		DirWork string `yaml:"dir-work" validate:"dirpath,required"`

		// GoFlags are the CLI arguments to be passed to the go compiler when
		// compiling the application server.
		GoFlags SpaceSeparatedList `yaml:"go-flags"`

		// Flags are the CLI arguments to be passed to the application server.
		Flags SpaceSeparatedList `yaml:"flags"`

		// Host is the application server host address.
		// Example: "https://local.example.com:8080"
		Host string `yaml:"host"  validate:"url,required"`
	} `yaml:"app"`

	// Verbose enables verbose console logs when true.
	// Verbose doesn't affect app server logs.
	Verbose bool `yaml:"verbose"`

	// Debounce is the file watcher debounce duration.
	Debounce struct {
		// Templ is the template regeneration debounce duration.
		Templ time.Duration `yaml:"templ"`

		// Go is the Go recompilation debounce duration.
		Go time.Duration `yaml:"go"`
	} `yaml:"debounce"`

	// ProxyTimeout defines for how long the proxy must try retry
	// requesting the application server when receiving connection refused error.
	ProxyTimeout time.Duration `yaml:"proxy-timeout"`

	// Lint runs golangci-lint before building if enabled.
	Lint bool `yaml:"lint"`

	// TemplierHost is the Templiér HTTP server host address.
	// Example: "127.0.0.1:9999".
	TemplierHost string `yaml:"templier-host" validate:"url,required"`

	// PrintJSDebugLogs enables Templiér injected javascript debug logs in the browser.
	PrintJSDebugLogs bool `yaml:"print-js-debug-logs"`

	// TLS is optional, will serve HTTP instead of HTTPS if nil.
	TLS *struct {
		Cert string `yaml:"cert" validate:"filepath,required"`
		Key  string `yaml:"key" validate:"filepath,required"`
	} `yaml:"tls"`
}

type ExludeFiles []string

func (e ExludeFiles) Validate() error {
	for i, expr := range config.App.Exclude {
		if _, err := glob.Compile(expr); err != nil {
			return fmt.Errorf("at index %d: %w", i, err)
		}
	}
	return nil
}

func mustParseConfig() {
	var fConfigPath string
	flag.StringVar(&fConfigPath, "config", "./templier.yml", "config file path")
	flag.Parse()

	// Set default config
	config.App.DirSrcRoot = "./"
	config.App.DirCmd = "./"
	config.App.DirWork = "./"
	config.Debounce.Templ = 50 * time.Millisecond
	config.Debounce.Go = 50 * time.Millisecond
	config.ProxyTimeout = 2 * time.Second
	config.Lint = true
	config.PrintJSDebugLogs = false
	config.TLS = nil

	if fConfigPath != "" {
		err := yamagiconf.LoadFile(fConfigPath, &config)
		if err != nil {
			panic(fmt.Errorf("reading config file: %w", err))
		}
	}

	workingDir, err := os.Getwd()
	if err != nil {
		panic(fmt.Errorf("getting working dir: %w", err))
	}
	config.serverOutPath = path.Join(os.TempDir(), workingDir)

	config.App.dirSrcRootAbsolute, err = filepath.Abs(config.App.DirSrcRoot)
	if err != nil {
		panic(fmt.Errorf("getting absolute path for app.dir-src-root: %w", err))
	}
}

type SpaceSeparatedList []string

var _ encoding.TextUnmarshaler = &SpaceSeparatedList{}

func (l *SpaceSeparatedList) UnmarshalText(t []byte) error {
	if string(t) == "" {
		return nil
	}
	*l = strings.Fields(string(t))
	return nil
}
