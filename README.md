<a href="https://goreportcard.com/report/github.com/romshark/templier">
    <img src="https://goreportcard.com/badge/github.com/romshark/templier" alt="GoReportCard">
</a>

# Templiér

Templiér is a Go web frontend development environment for
[Templ](https://github.com/a-h/templ)

- Watches your `.templ` files and rebuilds them.
- Watches all non-template files, rebuilds and restarts the server ✨.
- Automatically reloads your browser tabs when the server restarts or templates change.
- Runs [golangci-lint](https://golangci-lint.run/) if enabled.
- Reports all errors directly to all open browser tabs ✨.
- Shuts your server down gracefully.
- Displays application server console logs in the terminal.
- Supports templ's debug mode for fast live reload.
- Avoids reloading when files didn't change by keeping track of hashsums.
- Allows arbitrary CLI commands to be defined as [custom watchers](#custom-watchers) ✨.

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
- 📁 Templiér watches **all** file changes recursively
  (except for those that match `app.exclude`), recompiles and restarts the server
  (unless prevented by a [custom watcher](#custom-watchers)).
  Editing an embedded `.json` file in your app?
  Updating go mod? Templiér will notice, rebuild, restart and reload the browser
  tab for you automatically!
- 🖥️ Templiér shows Templ, Go compiler and [golangci-lint](https://golangci-lint.run/)
  errors (if any), and any errors from [custom watchers](#custom-watchers) in the browser.
  Templ's watcher just prints errors to the stdout and continues to display
  the last valid state.
- ⚙️ Templiér provides more configuration options (TLS, debounce, exclude globs, etc.).

## Custom Watchers 👁️👁️

Custom configurable watchers allow altering the behavior of Templiér for files
that match any of the `include` globs and they can be used for various use cases
demonstrated below.

The `requires` option allows overwriting the default behavior:

- empty field/string: no action, just execute Cmd.
- `reload`: Only reloads all browser tabs.
- `restart`: Restarts the server without rebuilding.
- `rebuild`: Requires the server to be rebuilt and restarted (standard behavior).

If custom watcher `A` requires `reload` but custom watcher `B` requires `rebuild` then
`rebuild` will be chosen once all custom watchers have finished executing.

### Custom Watcher Example: JavaScript Bundler

The following custom watcher will watch for `.js` file updates and automatically run
the CLI command `npm run js:bundle`, after which all browser tabs will be reloaded
using `requires: reload`. `fail-on-error: true` specifies that if `eslint` or `esbuild`
fail in the process, their error output will be shown directly in the browser.

```yaml
custom-watchers:
  - name: Bundle JS
    cmd: npm run bundle:js
    include: ["*.js"]
    fail-on-error: true
    debounce:
    # reload browser after successful bundling
    requires: reload
```

The `cmd` above refers to a script defined in `package.json` scripts:

```json
"scripts": {
  "bundle:js": "eslint . && esbuild --bundle --minify --outfile=./dist.js server/js/bundle.js",
  "lint:js": "eslint ."
},
```

### Custom Watcher Example: TailwindCSS and PostCSS

[TailwindCSS](https://tailwindcss.com/) and [PostCSS](https://postcss.org/) are often
used to simplify CSS styling and a custom watcher enables Templiér to hot-reload the
styles on changes:

First, configure `postcss.config.js`:

```js
module.exports = {
  content: [
    "./server/**/*.templ", // Include any .templ files
  ],
  plugins: [require("tailwindcss"), require("autoprefixer")],
};
```

and `tailwind.config.js`:

```js
/** @type {import('tailwindcss').Config} */
module.exports = {
  content: ["./**/*.{html,js,templ}"],
  theme: {
    extend: {},
  },
  plugins: [require("tailwindcss"), require("autoprefixer")],
};
```

Create a `package.json` file and install all necessary dev-dependencies

```sh
npm install tailwindcss postcss postcss-cli autoprefixer --save-dev
```

Add the scripts to `package.json` (where `input.css` is your main CSS
file containing your global custom styles and `public/dist.css` is the built CSS
output file that's linked to in your HTML):

```json
"scripts": {
  "build:css": "postcss ./input.css -o ./public/dist.css",
  "watch:css": "tailwindcss -i ./input.css -o ./public/dist.css --watch"
},
```

Finally, define a Templiér custom watcher to watch all Templ and CSS files and rebuild:

```yaml
- name: "Build CSS"
  cmd: npm run build:css
  include: ["*.templ", "input.css"]
  fail-on-error: true
  debounce:
  requires: reload
```

NOTE: if your `dist.css` is embedded, you may need to use `requires: rebuild`.

### Custom Watcher Example: Reload on config change.

Normally, Templiér rebuilds and restarts the server when any file changes (except for
`.templ` and `_templ.txt` files). However, when a config file changes we don't usually
require rebuilding the server. Restarting the server may be sufficient in this case:

```yaml
- name: Restart server on config change
  cmd: # No command, just restart
  include: ["*.toml"] # Any TOML file
  fail-on-error:
  debounce:
  requires: restart
```

## How Templiér works

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

## Development

### Building

You can build Templiér using the following command:

```sh
go build -o templier ./bin/templier
```

If you're adding bin library to your path, you can just execute the binary.

zsh:
```zsh
export PATH=$(pwd)/bin:$PATH
```

[fish](https://fishshell.com/):
```fish
fish_add_path (pwd)/bin
```
