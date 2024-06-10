# Templiér

Templiér is a Go web frontend development environment for
[Templ](https://github.com/a-h/templ)

- Watches your `.templ` files and rebuilds them.
- Watches all non-template files, rebuilds and restarts the server.
- Automatically reload your browser tabs when the server restarts.
- Runs [golangci-lint](https://golangci-lint.run/) if enabled.
- Reports all errors directly to all open browser tabs.
- Shuts your server down gracefully.
- Displays application server console logs in the terminal.

## How it works

Templiér acts as a file watcher, proxy server and process manager.
Once Templiér is started, it begins watching files in the `app.dir-src-root` directory.
On start and on file change, it automatically builds your application server executable
assuming that the main package is specified by the `app.dir-cmd` directory. Any custom
Go compiler CLI arguments can be specified by `app.go-flags`. Once built,
the application server executable is launched with `app.flags` CLI parameters and
the working directory set to `app.dir-work`. When necessary, the application server
process is shut down gracefully, rebuilt, linted and restarted.

Templiér hosts your application under the URL specified by `templier-host` and proxies
all requests to the application server process that it launched injecting Templiér
JavaScript that opens a websocket connection to Templiér from the browser tab to listen
for events and reload or display necessary status information when necessary.
In the CLI console logs, all Templiér logs are prefixed with 🤖,
while application server logs are displayed without the prefix.
