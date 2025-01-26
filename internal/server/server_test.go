package server_test

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/romshark/templier/internal/server"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
)

func TestRenderErrPage(t *testing.T) {
	var buf bytes.Buffer
	err := server.RenderErrpage(
		context.Background(), &buf, "test", []server.Report{
			{Subject: "Test Subject", Body: "Test Body"},
		}, true,
	)
	require.NoError(t, err)

	_, err = html.Parse(bytes.NewReader(buf.Bytes()))
	require.NoError(t, err)
}

func TestMustRenderJSInjection(t *testing.T) {
	jsInjection := server.MustRenderJSInjection(context.Background(), true)
	require.NotEmpty(t, jsInjection)

	_, err := html.Parse(bytes.NewReader(jsInjection))
	require.NoError(t, err)
}

func TestInjectInBody(t *testing.T) {
	jsInjection := []byte(`<script>console.log("injection")</script>`)

	f := func(t *testing.T, bodyInputFilePath, expectBodyOutputFilePath string) {
		t.Helper()

		body, err := os.ReadFile(bodyInputFilePath)
		require.NoError(t, err)

		expected, err := os.ReadFile(expectBodyOutputFilePath)
		require.NoError(t, err)

		originalInjectionBytes := string(jsInjection)

		var buf bytes.Buffer
		err = server.WriteWithInjection(&buf, []byte(body), jsInjection)
		require.NoError(t, err)
		actual := buf.String()
		require.Equal(t, string(expected), actual)
		require.Equal(t, originalInjectionBytes, string(jsInjection),
			"mutation of original injection bytes")
	}

	f(t,
		"testdata/empty_input.html",
		"testdata/empty_expect.html",
	)
	f(t,
		"testdata/injectiontarget_input.html",
		"testdata/injectiontarget_expect.html",
	)
	f(t,
		"testdata/no_head_input.html",
		"testdata/no_head_expect.html",
	)
	f(t,
		"testdata/nonhtml_input.txt",
		"testdata/nonhtml_expect.html",
	)
	f(t,
		"testdata/uppercase_body_input.html",
		"testdata/uppercase_body_expect.html",
	)
	f(t,
		"testdata/uppercase_head_input.html",
		"testdata/uppercase_head_expect.html",
	)
}
