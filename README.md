<a href="https://goreportcard.com/report/github.com/romshark/templier">
    <img src="https://goreportcard.com/badge/github.com/romshark/templier" alt="GoReportCard">
</a>

# Templiér

Templiér is a Go web frontend development environment for
[Templ](https://github.com/a-h/templ)

- Watches your `.templ` files and rebuilds them.
- Watches all non-template files, rebuilds and restarts the server.
- Automatically reloads your browser tabs when the server restarts or templates change.
- Runs [golangci-lint](https://golangci-lint.run/) if enabled.
- Reports all errors directly to all open browser tabs.
- Shuts your server down gracefully.
- Displays application server console logs in the terminal.
- Supports templ's debug mode for fast live reload.

## Quick Start

Install Templiér:
```sh
go install github.com/romshark/templier@latest 
```
Then copy-paste [example-config.yml](https://github.com/romshark/templier/blob/main/example-config.yml) to your project source folder as `templier.yml`, edit to your needs and run:

```sh
templier --config ./templier.yml
```

## How is Templiér different from templ's own watch mode?

As you may already know, templ supports [live reload](https://templ.guide/commands-and-tools/live-reload)
out of the box using `templ generate --watch --proxy="http://localhost:8080" --cmd="go run ."`,
which is great, but Templiér provides even better developer experience:

- 🥶 Templiér doesn't become unresponsive when the Go code fails to compile,
  instead it prints the compiler error output to the browser tab and keeps watching.
  Once you fixed the Go code, Templiér will reload and work as usual with no intervention.
  In contrast, templ's watcher needs to be restarted manually.
- 📁 Templiér watches **all** file changes recursively and recompiles the server.
  Editing an embedded `.json` file in your app?
  Updating go mod? Templiér will notice and rebuild.
- 🖥️ Templiér shows Templ, Go compiler and [golangci-lint](https://golangci-lint.run/)
  errors (if any) in the browser. Templ's watcher just prints errors to the stdout and
  continues to display the last valid state.
- ⚙️ Templiér provides more configuration options (TLS, debounce, exclude globs, etc.).

## How it works

Templiér acts as a file watcher, proxy server and process manager.
Once Templiér is started, it runs `templ generate --watch` in the background and begins
watching files in the `app.dir-src-root` directory.
On start and on file change, it automatically builds your application server executable
saving it in the OS' temp directory (cleaned up latest before exiting) assuming that
the main package is specified by the `app.dir-cmd` directory. Any custom Go compiler
CLI arguments can be specified by `app.go-flags`. Once built, the application server
executable is launched with `app.flags` CLI parameters and the working directory
set to `app.dir-work`. When necessary, the application server process is shut down
gracefully, rebuilt, linted and restarted.

Templiér ignores changes made to `.templ`, `_templ.go` and `_templ.txt` files and lets
`templ generate --watch` do its debug mode magic allowing for lightning fast reloads
when a templ template changed with no need to rebuild the server.

Templiér hosts your application under the URL specified by `templier-host` and proxies
all requests to the application server process that it launched injecting Templiér
JavaScript that opens a websocket connection to Templiér from the browser tab to listen
for events and reload or display necessary status information when necessary.
In the CLI console logs, all Templiér logs are prefixed with 🤖,
while application server logs are displayed without the prefix.
