package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"

	"github.com/romshark/templier/engine"
	"github.com/romshark/templier/internal/config"
	"github.com/romshark/templier/internal/log"
)

func main() {
	conf := config.MustParse()

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
	})
	if err != nil {
		log.Fatalf("%v", err)
	}
	if err := eng.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("%v", err)
	}
}
