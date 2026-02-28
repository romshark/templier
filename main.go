package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"

	"github.com/romshark/templier/engine"
	"github.com/romshark/templier/internal/config"
	"github.com/romshark/templier/internal/log"
)

// Set by goreleaser via -ldflags -X.
var version, commit, date string

func main() {
	// When built with "go install" the ldflags are not set.
	// Fall back to the build info embedded by the Go toolchain.
	if version == "" {
		if info, ok := debug.ReadBuildInfo(); ok {
			version = info.Main.Version
			for _, s := range info.Settings {
				switch s.Key {
				case "vcs.revision":
					commit = s.Value
				case "vcs.time":
					date = s.Value
				}
			}
		}
	}

	conf := config.MustParse(version, commit, date)

	// Map config log level to slog level.
	var lvl slog.LevelVar
	switch conf.Log.Level {
	case engine.LogLevelDebug:
		lvl.Set(slog.LevelDebug)
	case engine.LogLevelVerbose:
		lvl.Set(slog.LevelInfo)
	default:
		lvl.Set(slog.LevelError)
	}
	logger := slog.New(log.NewHandler(os.Stdout, &lvl))

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	eng, err := engine.New(conf, engine.Options{
		Logger:    logger,
		ClearLogs: log.ClearLogs,
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
		Version:   version,
		Commit:    commit,
		Date:      date,
	})
	if err != nil {
		log.Fatalf("%v", err)
	}
	if err := eng.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("%v", err)
	}
}
