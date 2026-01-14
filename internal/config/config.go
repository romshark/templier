package config

import (
	"bytes"
	"debug/buildinfo"
	"encoding"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/romshark/templier/internal/action"
	"github.com/romshark/templier/internal/log"

	"github.com/gobwas/glob"
	"github.com/romshark/yamagiconf"
)

const (
	Version               = "0.10.22"
	SupportedTemplVersion = "v0.3.977"
)

var config Config

type Config struct {
	// Compiler defines optional Go compiler flags
	Compiler *ConfigCompiler `yaml:"compiler"`

	App ConfigApp `yaml:"app"`

	// Log specifies logging related configurations.
	Log ConfigLog `yaml:"log"`

	// Debounce is the file watcher debounce duration.
	Debounce time.Duration `yaml:"debounce"`

	// ProxyTimeout defines for how long the proxy must try retry
	// requesting the application server when receiving connection refused error.
	ProxyTimeout time.Duration `yaml:"proxy-timeout"`

	// Lint runs golangci-lint before building if enabled.
	Lint bool `yaml:"lint"`

	// Format enables running `templ fmt` on `.templ` file changes.
	Format bool `yaml:"format"`

	// TemplierHost is the Templiér HTTP server host address.
	// Example: "127.0.0.1:9999".
	TemplierHost string `yaml:"templier-host" validate:"url,required"`

	// TLS is optional, will serve HTTP instead of HTTPS if nil.
	TLS *struct {
		Cert string `yaml:"cert" validate:"filepath,required"`
		Key  string `yaml:"key" validate:"filepath,required"`
	} `yaml:"tls"`

	// CustomWatchers defines custom file change watchers.
	CustomWatchers []ConfigCustomWatcher `yaml:"custom-watchers"`
}

type ConfigApp struct {
	// DirSrcRoot is the source root directory for the application server.
	DirSrcRoot string `yaml:"dir-src-root" validate:"dirpath,required"`

	dirSrcRootAbsolute string `yaml:"-"` // Initialized from DirSrcRoot

	// Exclude defines glob expressions to match files exluded from watching.
	Exclude GlobList `yaml:"exclude"`

	// DirCmd is the server cmd directory containing the `main` function.
	DirCmd string `yaml:"dir-cmd" validate:"dirpath,required"`

	// DirWork is the working directory to run the application server from.
	DirWork string `yaml:"dir-work" validate:"dirpath,required"`

	// Flags are the CLI arguments to be passed to the application server.
	Flags SpaceSeparatedList `yaml:"flags"`

	// Host is the application server host address.
	// Example: "https://local.example.com:8080"
	Host URL `yaml:"host" validate:"required"`
}

func (c *ConfigApp) DirSrcRootAbsolute() string { return c.dirSrcRootAbsolute }

func (c *Config) CompilerFlags() []string {
	if c.Compiler != nil {
		return c.Compiler.flags
	}
	return nil
}

func (c *Config) CompilerEnv() []string {
	if c.Compiler != nil {
		return c.Compiler.env
	}
	return nil
}

type ConfigCompiler struct {
	// Gcflags is the -gcflags compiler flags to be passed to the go
	// compiler when compiling the application server.
	Gcflags string `yaml:"gcflags"`

	// Ldflags provides the -ldflags CLI argument to Go compiler
	// to pass on each go tool link invocation.
	Ldflags string `yaml:"ldflags"`

	// Asmflags is equivalent to `-asmflags '[pattern=]arg list'`.
	Asmflags string `yaml:"asmflags"`

	// Tags lists additional build tags to consider satisfied during the build.
	Tags []string `yaml:"tags"`

	// Race sets `-race` when true.
	Race bool `yaml:"race"`

	// Trimpath sets `-trimpath` when true.
	Trimpath bool `yaml:"trimpath"`

	// Msan sets `-msan` when true.
	Msan bool `yaml:"msan"`

	// P sets the number of programs, such as build commands that can be run in
	// parallel. The default is GOMAXPROCS, normally the number of CPUs available.
	P uint32 `yaml:"p"`

	// Env passes environment variables to the Go compiler.
	Env map[string]string `yaml:"env"`

	env []string `yaml:"-"` // Initialized from Env.

	flags []string `yaml:"-"` // Initialized from all of the above.
}

type ConfigLog struct {
	// Level accepts either of:
	//  - "": empty string is the same as "erronly"
	//  - "erronly": error logs only.
	//  - "verbose": verbose logging of relevant events.
	//  - "debug": verbose debug logging.
	Level LogLevel `yaml:"level"`

	// ClearOn accepts either of:
	//  - "": disables console log clearing.
	//  - "restart": clears console logs only on app server restart.
	//  - "file-change": clears console logs on every file change.
	ClearOn LogClear `yaml:"clear-on"`

	// PrintJSDebugLogs enables Templiér injected javascript
	// debug logs in the browser.
	PrintJSDebugLogs bool `yaml:"print-js-debug-logs"`
}

type ConfigCustomWatcher struct {
	// Name is the display name for the custom watcher.
	Name TrimmedString `yaml:"name"`

	// Include specifies glob expressions for what files to watch.
	Include GlobList `yaml:"include"`

	// Exclude specifies glob expressions for what files to ignore
	// that would otherwise match `include`.
	Exclude GlobList `yaml:"exclude"`

	// Cmd specifies the command to run when an included file changed.
	// Cmd will be executed in app.dir-work. This is optional and can be left empty
	// since sometimes all you want to do is rebuild & restart or just restart
	// the server, such as when a config file changes.
	Cmd CmdStr `yaml:"cmd"`

	// FailOnError specifies that in case cmd returns error code 1 the output
	// of the execution should be displayed in the browser, just like
	// for example if the Go compiler fails to compile.
	FailOnError bool `yaml:"fail-on-error"`

	// Debounce defines how long to wait for more file changes
	// after the first one occurred before executing cmd.
	// Default debounce duration is applied if left empty.
	Debounce time.Duration `yaml:"debounce"`

	// Requires defines what action is required when an included file changed.
	// Accepts the following options:
	//
	//  - "" (or simply keep the field empty): no action, just execute Cmd.
	//  - "reload": Requires browser tabs to be reloaded.
	//  - "restart": Requires the server process to be restarted.
	//  - "rebuild": Requires the server to be rebuilt and restarted.
	//
	// This option overwrites regular behavior (for non-templ file changes it's "rebuild")
	Requires Requires `yaml:"requires"`
}

func (w ConfigCustomWatcher) Validate() error {
	if w.Name == "" {
		return errors.New("custom watcher has no name")
	}
	if w.Requires == Requires(action.ActionNone) && w.Cmd == "" {
		return fmt.Errorf("custom watcher %q requires no action, hence "+
			" cmd must not be empty", w.Name)
	}
	return nil
}

// TrimmedString removes all leading and trailing white space,
// as defined by Unicode, when parsing from text as TextUnmarshaler.
type TrimmedString string

var _ encoding.TextUnmarshaler = new(TrimmedString)

func (t *TrimmedString) UnmarshalText(text []byte) error {
	*t = TrimmedString(bytes.TrimSpace(text))
	return nil
}

type LogLevel log.LogLevel

func (l *LogLevel) UnmarshalText(text []byte) error {
	switch string(text) {
	case "", "erronly":
		*l = LogLevel(log.LogLevelErrOnly)
	case "verbose":
		*l = LogLevel(log.LogLevelVerbose)
	case "debug":
		*l = LogLevel(log.LogLevelDebug)
	default:
		return fmt.Errorf(`invalid log option %q, `+
			`use either of: ["" (same as erronly), "erronly", "verbose", "debug"]`,
			string(text))
	}
	return nil
}

type LogClear int8

const (
	LogClearDisabled LogClear = iota
	LogClearOnRestart
	LogClearOnFileChange
)

func (l *LogClear) UnmarshalText(text []byte) error {
	switch string(text) {
	case "":
		*l = LogClearDisabled
	case "restart":
		*l = LogClearOnRestart
	case "file-change":
		*l = LogClearOnFileChange
	default:
		return fmt.Errorf(`invalid clear-on option %q, `+
			`use either of: ["" (disable), "restart", "file-change"]`,
			string(text))
	}
	return nil
}

type Requires action.Type

func (r *Requires) UnmarshalText(text []byte) error {
	switch string(text) {
	case "":
		*r = Requires(action.ActionNone)
	case "reload":
		*r = Requires(action.ActionReload)
	case "restart":
		*r = Requires(action.ActionRestart)
	case "rebuild":
		*r = Requires(action.ActionRebuild)
	default:
		return fmt.Errorf(`invalid requires action %q, `+
			`use either of: ["" (empty, no action), "reload", "restart", "rebuild"]`,
			string(text))
	}
	return nil
}

type CmdStr string

func (c *CmdStr) UnmarshalText(t []byte) error {
	*c = CmdStr(bytes.Trim(t, " \t\n\r"))
	return nil
}

// Cmd returns only the command without arguments.
func (c CmdStr) Cmd() string {
	if c == "" {
		return ""
	}
	return strings.Fields(string(c))[0]
}

type URL struct{ URL *url.URL }

func (u *URL) UnmarshalText(t []byte) error {
	x, err := url.Parse(string(t))
	if err != nil {
		return err
	}
	u.URL = x
	return nil
}

type GlobList []string

func (e GlobList) Validate() error {
	for i, expr := range config.App.Exclude {
		if _, err := glob.Compile(expr); err != nil {
			return fmt.Errorf("at index %d: %w", i, err)
		}
	}
	return nil
}

func PrintVersionInfoAndExit() {
	defer os.Exit(0)

	p, err := exec.LookPath(os.Args[0])
	if err != nil {
		fmt.Printf("resolving executable file path: %v\n", err)
		os.Exit(1)
	}

	f, err := os.Open(p)
	if err != nil {
		fmt.Printf("opening executable file %q: %v\n", os.Args[0], err)
		os.Exit(1)
	}

	info, err := buildinfo.Read(f)
	if err != nil {
		fmt.Printf("Reading build information: %v\n", err)
	}

	fmt.Printf("Templiér v%s\n\n", Version)
	fmt.Printf("%v\n", info)
}

func MustParse() *Config {
	var fVersion bool
	var fConfigPath string
	flag.BoolVar(&fVersion, "version", false, "show version")
	flag.StringVar(&fConfigPath, "config", "", "config file path")
	flag.Parse()

	log.Debugf("reading config file: %q", fConfigPath)

	if fVersion {
		PrintVersionInfoAndExit()
	}

	// Set default config.
	config.App.DirSrcRoot = "./"
	config.App.DirCmd = "./"
	config.App.DirWork = "./"
	config.Debounce = 50 * time.Millisecond
	config.ProxyTimeout = 2 * time.Second
	config.Lint = true
	config.Format = true
	config.Log.Level = LogLevel(log.LogLevelErrOnly)
	config.Log.ClearOn = LogClearDisabled
	config.Log.PrintJSDebugLogs = false
	config.TLS = nil

	if fConfigPath == "" {
		// Try to detect config automatically.
		if _, err := os.Stat("templier.yml"); err == nil {
			fConfigPath = "templier.yml"
		} else if _, err := os.Stat("templier.yaml"); err == nil {
			fConfigPath = "templier.yaml"
		} else {
			log.Fatalf("couldn't find config file: templier.yml")
		}
	}
	err := yamagiconf.LoadFile(fConfigPath, &config)
	if err != nil {
		log.Fatalf("reading config file: %v", err)
	}

	// Set default watch debounce
	for i := range config.CustomWatchers {
		if config.CustomWatchers[i].Debounce == 0 {
			config.CustomWatchers[i].Debounce = 50 * time.Millisecond
		}
	}

	config.App.dirSrcRootAbsolute, err = filepath.Abs(config.App.DirSrcRoot)
	if err != nil {
		log.Fatalf("getting absolute path for app.dir-src-root: %v", err)
	}

	if c := config.Compiler; c != nil {
		c.flags = []string{}
		if c.Gcflags != "" {
			c.flags = append(c.flags, "-gcflags", c.Gcflags)
		}
		if c.Tags != nil {
			c.flags = append(c.flags, "-tags", strings.Join(c.Tags, ","))
		}
		if c.Ldflags != "" {
			c.flags = append(c.flags, "-ldflags", c.Ldflags)
		}
		if c.Race {
			c.flags = append(c.flags, "-race")
		}
		if c.Msan {
			c.flags = append(c.flags, "-msan")
		}
		if c.Trimpath {
			c.flags = append(c.flags, "-trimpath")
		}
		if c.P != 0 {
			c.flags = append(c.flags, "-p", strconv.Itoa(int(c.P)))
		}
		c.env = make([]string, 0, len(c.Env))
		for k, v := range c.Env {
			c.env = append(c.env, k+"="+v)
		}
	}

	log.Debugf("set source directory absolute path: %q", config.App.dirSrcRootAbsolute)
	return &config
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
