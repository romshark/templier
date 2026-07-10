package server_test

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/romshark/templier/internal/server"
	"github.com/alecthomas/assert/v2"
	"golang.org/x/net/html"
)

func TestRenderErrPage(t *testing.T) {
	var buf bytes.Buffer
	err := server.RenderErrpage(
		context.Background(), &buf, "test", []server.Report{
			{Subject: "Test Subject", Body: "Test Body"},
		}, true, "reconnecting...",
	)
	assert.NoError(t, err)

	_, err = html.Parse(bytes.NewReader(buf.Bytes()))
	assert.NoError(t, err)
}

func TestMustRenderJSInjection(t *testing.T) {
	jsInjection := server.MustRenderJSInjection(context.Background(), true, "reconnecting...")
	assert.NotZero(t, len(jsInjection))

	_, err := html.Parse(bytes.NewReader(jsInjection))
	assert.NoError(t, err)
}

func TestInjectInBody(t *testing.T) {
	jsInjection := []byte(`<script>console.log("injection")</script>`)

	f := func(t *testing.T, bodyInputFilePath, expectBodyOutputFilePath string) {
		t.Helper()

		body, err := os.ReadFile(bodyInputFilePath)
		assert.NoError(t, err)

		expected, err := os.ReadFile(expectBodyOutputFilePath)
		assert.NoError(t, err)

		originalInjectionBytes := string(jsInjection)

		var buf bytes.Buffer
		err = server.WriteWithInjection(&buf, []byte(body), jsInjection)
		assert.NoError(t, err)
		actual := buf.String()
		assert.Equal(t, string(expected), actual)
		assert.Equal(t, originalInjectionBytes, string(jsInjection),
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
