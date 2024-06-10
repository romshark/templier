package main

import (
	"testing"

	"github.com/romshark/yamagiconf"

	"github.com/stretchr/testify/require"
)

func TestValidateType(t *testing.T) {
	err := yamagiconf.ValidateType[Config]()
	require.NoError(t, err)
}

func TestSpaceSeparatedList(t *testing.T) {
	type TestConfig struct {
		List SpaceSeparatedList `yaml:"list"`
	}
	for _, td := range []struct {
		name   string
		input  string
		expect SpaceSeparatedList
	}{
		{
			name:   "empty",
			input:  "list:",
			expect: SpaceSeparatedList(nil),
		},
		{
			name:   "empty_explicit",
			input:  "list: ''",
			expect: SpaceSeparatedList(nil),
		},
		{
			name:   "single",
			input:  "list: one_item",
			expect: SpaceSeparatedList{"one_item"},
		},
		{
			name:   "spaces",
			input:  "list: -flag value",
			expect: SpaceSeparatedList{"-flag", "value"},
		},
		{
			name:   "multiline",
			input:  "list: |\n  first second\n  third\tfourth",
			expect: SpaceSeparatedList{"first", "second", "third", "fourth"},
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
