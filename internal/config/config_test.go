package config_test

import (
	_ "embed"
	"testing"

	"github.com/romshark/templier/internal/config"

	"github.com/romshark/yamagiconf"
	"github.com/stretchr/testify/require"
)

func TestValidateType(t *testing.T) {
	err := yamagiconf.ValidateType[config.Config]()
	require.NoError(t, err)
}

func TestSpaceSeparatedList(t *testing.T) {
	t.Parallel()

	type TestConfig struct {
		List config.SpaceSeparatedList `yaml:"list"`
	}

	f := func(t *testing.T, input string, expect config.SpaceSeparatedList) {
		t.Helper()
		var actual TestConfig
		err := yamagiconf.Load(input, &actual)
		require.NoError(t, err)
		require.Equal(t, expect, actual.List)
	}

	// Empty.
	f(t, "list:", config.SpaceSeparatedList(nil))
	// Empty explicit.
	f(t, "list: ''", config.SpaceSeparatedList(nil))
	// Single.
	f(t, "list: one_item", config.SpaceSeparatedList{"one_item"})
	// Spaces.
	f(t, "list: -flag value", config.SpaceSeparatedList{"-flag", "value"})
	// Multiline.
	f(t, "list: |\n  first second\n  third\tfourth",
		config.SpaceSeparatedList{"first", "second", "third", "fourth"})
}
