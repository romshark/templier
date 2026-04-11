package ansihtml

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestConvert checks that ANSI text is turned into escaped HTML with the
// expected span wrappers, and that unsupported sequences are dropped.
func TestConvert(t *testing.T) {
	t.Run("styles and escaping", func(t *testing.T) {
		input := "5 < 6 & 7 " +
			"\x1b[1;31mRED" +
			"\x1b[2m+D" +
			"\x1b[22m plain" +
			"\x1b[39m end"

		want := "5 &lt; 6 &amp; 7 " +
			`<span class="ansi-bold ansi-fg-red">RED</span>` +
			`<span class="ansi-bold ansi-fg-red ansi-dim">+D</span>` +
			`<span class="ansi-fg-red"> plain</span>` +
			" end"

		require.Equal(t, want, Convert(input))
	})

	t.Run("malformed and unsupported sequences", func(t *testing.T) {
		tests := []struct {
			name  string
			input string
			want  string
		}{
			{
				name:  "lone escape at end",
				input: "a\x1b",
				want:  "a",
			},
			{
				name:  "non csi escape",
				input: "a\x1bXb",
				want:  "ab",
			},
			{
				name:  "unterminated csi",
				input: "a\x1b[31",
				want:  "a",
			},
			{
				name:  "unsupported csi is dropped",
				input: "a\x1b[2Kb",
				want:  "ab",
			},
			{
				name:  "empty sgr resets without output",
				input: "a\x1b[mb",
				want:  "ab",
			},
			{
				name:  "invalid and unknown sgr params are ignored",
				input: "a\x1b[;99;1;0mb",
				want:  "ab",
			},
			{
				name:  "open span is closed at end of string",
				input: "\x1b[32mgreen",
				want:  `<span class="ansi-fg-green">green</span>`,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				require.Equal(t, tt.want, Convert(tt.input))
			})
		}
	})
}

// TestApplySGR checks each supported SGR code directly without going through
// escape-sequence parsing.
func TestApplySGR(t *testing.T) {
	tests := []struct {
		name string
		open []string
		code int
		want []string
	}{
		{
			name: "reset clears everything",
			open: []string{"ansi-bold", "ansi-fg-red"},
			code: 0,
			want: nil,
		},
		{
			name: "bold is added once",
			open: []string{"ansi-bold"},
			code: 1,
			want: []string{"ansi-bold"},
		},
		{
			name: "dim is added",
			open: []string{"ansi-bold"},
			code: 2,
			want: []string{"ansi-bold", "ansi-dim"},
		},
		{
			name: "normal intensity removes bold and dim",
			open: []string{"ansi-bold", "ansi-dim", "ansi-fg-red"},
			code: 22,
			want: []string{"ansi-fg-red"},
		},
		{
			name: "default foreground removes current color",
			open: []string{"ansi-bold", "ansi-fg-red"},
			code: 39,
			want: []string{"ansi-bold"},
		},
		{
			name: "foreground color replaces previous one",
			open: []string{"ansi-bold", "ansi-fg-red"},
			code: 32,
			want: []string{"ansi-bold", "ansi-fg-green"},
		},
		{
			name: "unknown codes leave state unchanged",
			open: []string{"ansi-bold"},
			code: 99,
			want: []string{"ansi-bold"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := applySGR(append([]string(nil), tt.open...), tt.code)
			require.Equal(t, tt.want, got)
		})
	}
}
