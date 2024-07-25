package main

import (
	_ "embed"
	"testing"

	"github.com/romshark/templier/internal/config"
	"github.com/romshark/yamagiconf"
	"github.com/stretchr/testify/require"
)

//go:embed example-config.yml
var exampleConfig string

func TestExampleConfig(t *testing.T) {
	var c config.Config

	err := yamagiconf.Load(exampleConfig, &c)
	require.NoError(t, err)
}
