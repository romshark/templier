package main

import (
	"encoding"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/romshark/yamagiconf"
)

type Config struct {
	App struct {
		// DirSrcRoot is the source root directory for the application server.
		DirSrcRoot string `yaml:"dir-src-root" validate:"dirpath,required"`

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

var (
	serverOutPath string
	config        Config
)

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
