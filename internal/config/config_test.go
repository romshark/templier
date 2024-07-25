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
	type TestConfig struct {
		List config.SpaceSeparatedList `yaml:"list"`
	}
	for _, td := range []struct {
		name   string
		input  string
		expect config.SpaceSeparatedList
	}{
		{
			name:   "empty",
			input:  "list:",
			expect: config.SpaceSeparatedList(nil),
		},
		{
			name:   "empty_explicit",
			input:  "list: ''",
			expect: config.SpaceSeparatedList(nil),
		},
		{
			name:   "single",
			input:  "list: one_item",
			expect: config.SpaceSeparatedList{"one_item"},
		},
		{
			name:   "spaces",
			input:  "list: -flag value",
			expect: config.SpaceSeparatedList{"-flag", "value"},
		},
		{
			name:   "multiline",
			input:  "list: |\n  first second\n  third\tfourth",
			expect: config.SpaceSeparatedList{"first", "second", "third", "fourth"},
		},
	} {
		t.Run(td.name, func(t *testing.T) {
			var actual TestConfig
			err := yamagiconf.Load(td.input, &actual)
			require.NoError(t, err)
			require.Equal(t, td.expect, actual.List)
		})
	}
}
