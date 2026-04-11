<a href="https://goreportcard.com/report/github.com/romshark/templier">
    <img src="https://goreportcard.com/badge/github.com/romshark/templier" alt="GoReportCard">
</a>

<br>
<br>
<picture>
  <source media="(prefers-color-scheme: dark)" srcset="https://raw.githubusercontent.com/romshark/templier/5c68f5b74984b6a2c6aec66912f6852819acbf9e/logo_white.svg">
  <img src="https://raw.githubusercontent.com/romshark/templier/5c68f5b74984b6a2c6aec66912f6852819acbf9e/logo_color.svg" alt="Logo" width="400">
</picture>

Templiér is a Go web frontend development environment for
[Templ](https://github.com/a-h/templ)
that works similarly to templ's native
[`--watch`](https://templ.guide/developer-tools/live-reload/) mode but provides
more functionality and reports errors directly to all open browser tabs ✨.

Templiér allows arbitrary CLI commands to be defined as [custom watchers](#custom-watchers) ✨.
  - example: [Bundle JavaScript](#custom-watcher-example-javascript-bundler)
  - example: [Rebuild CSS](#custom-watcher-example-tailwindcss-and-postcss)
  - example: [Restart on config change](#custom-watcher-example-reload-on-config-change)

Templiér is an integral part of [github.com/romshark/datapages](https://github.com/romshark/datapages/).

## Quick Start

Install Templiér:

```sh
go install github.com/romshark/templier@latest
```

Then copy [example-config.yml](https://github.com/romshark/templier/blob/main/example-config.yml) to your project source folder as `templier.yml`, adjust it to your needs, and run:

```sh
templier --config ./templier.yml
```

ℹ️ Templiér automatically detects `templier.yml` and `templier.yaml` in the current directory without requiring the explicit `--config` flag.

## How is Templiér different from templ's own watch mode?

As you may already know, templ supports [live reload](https://templ.guide/commands-and-tools/live-reload)
out of the box using `templ generate --watch --proxy="http://localhost:8080" --cmd="go run ."`,
which is great, but Templiér provides an even better developer experience:

- 🥶 Templiér doesn't become unresponsive when the Go code fails to compile,
  instead it prints the compiler error output to the browser tab with ANSI
  colors preserved and keeps watching.
  Once you've fixed the Go code, Templiér will reload and work as usual with no intervention.
  In contrast, templ's watcher needs to be restarted manually.
- 📁 Templiér watches **all** file changes recursively,
  recompiles and restarts the server
  (unless the file matches `app.exclude` or is handled by a [custom watcher](#custom-watchers)).
  Editing an embedded `.json` file in your app?
  Updating `go.mod`? Templiér will notice, rebuild, restart, and reload the browser
  tab for you automatically!
- 🖥️ Templiér shows Templ, Go compiler and [golangci-lint](https://golangci-lint.run/)
  errors, if any, and errors from [custom watchers](#custom-watchers) in the browser.
  Templ's watcher just prints errors to the stdout and continues to display
  the last valid state.
- ⚙️ Templiér provides more configuration options (TLS, debounce, exclude globs, etc.).

## Custom Watchers 👁️👁️

Custom watchers let you change how Templiér behaves for files that match any of
the `include` globs, and they can be used for the use cases shown below.

The `requires` option lets you override the default behavior:

- empty field/string: no extra action, just execute `cmd`.
- `reload`: Only reload all open browser tabs.
- `restart`: Restarts the server without rebuilding.
- `rebuild`: Requires the server to be rebuilt and restarted (standard behavior).

If custom watcher `A` requires `reload` but custom watcher `B` requires `rebuild`,
`rebuild` will be chosen once all custom watchers have finished executing.

### Custom Watcher Example: JavaScript Bundler

The following custom watcher watches for `.js` file updates and automatically runs
the CLI command `npm run js:bundle`, after which all browser tabs will be reloaded
using `requires: reload`. `fail-on-error: true` means that if `eslint` or `esbuild`
fails, their error output will be shown directly in the browser.

```yaml
custom-watchers:
  - name: Bundle JS
    cmd: npm run bundle:js
    include: ["*.js"]
    exclude: ["path/to/your/dist.js"]
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
used to simplify CSS styling, and a custom watcher enables Templiér to hot-reload the
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

Create a `package.json` file and install all necessary dev dependencies:

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

Finally, define a Templiér custom watcher to watch all Templ and CSS files and reload the browser:

```yaml
- name: Build CSS
  cmd: npm run build:css
  include: ["*.templ", "input.css"]
  exclude: ["path/to/your/dist.css"]
  fail-on-error: true
  debounce:
  requires: reload
```

NOTE: if your `dist.css` is embedded, you may need to use `requires: rebuild`.

### Custom Watcher Example: Reload on config change

Normally, Templiér rebuilds and restarts the server when any file changes except
for `.templ` files. However, when a config file changes, you usually don't need
to rebuild the server. Restarting it may be sufficient:

```yaml
- name: Restart server on config change
  cmd: # No command, just restart
  include: ["*.toml"] # Any TOML file
  exclude:
  fail-on-error:
  debounce:
  requires: restart
```

## How Templiér works

Templiér acts as a file watcher, proxy server and process manager.
Once Templiér is started, it runs `templ generate --watch` in the background and begins
watching files in the `app.dir-src-root` directory.
On start and on file change, it automatically builds your application server executable
and saves it in the OS temp directory (cleaned up on exit at the latest), assuming that
the main package is specified by the `app.dir-cmd` directory.
Custom Go compiler arguments can be specified with `compiler`. Once built, the application server
executable is launched with `app.flags` CLI parameters and the working directory
set to `app.dir-work`. When necessary, the application server process is shut down
gracefully, rebuilt, linted and restarted.
On `.templ` file changes Templiér only tries to compile and lint the server code
without refreshing the page.

Templiér hosts your application under the URL specified by `templier-host` and proxies
all requests to the application server process it launches, injecting Templiér
JavaScript that opens a websocket connection back to Templiér from the browser tab
to listen for events and reload or display status information when needed.
In the CLI console logs, all Templiér logs are prefixed with 🤖,
while application server logs are displayed without the prefix.

## Development

Run the tests using `go test -race ./...` and use the latest version of
[golangci-lint](https://golangci-lint.run/) to ensure code integrity.

### Building

Install Templiér using the following command in the repository root directory:

```sh
go install .
```

This installs the `templier` binary into your Go bin directory.
If `templier` isn't found afterwards, make sure your Go bin directory is on
your `PATH`: https://go.dev/wiki/GOPATH

### Important Considerations

- Templiér currently doesn't support Windows.
- When measuring performance, make sure you're not running against the Templiér proxy
  that injects the JavaScript for auto-reloading because it will be slower and should
  only be used for development. Instead, use the direct host address of your application
  server specified by `app.host` in your `templier.yml` configuration.
- Templiér's JavaScript injection uses the `GET /__templier/events` HTTP endpoint for
  its websocket connection. Make sure it doesn't collide with your application's paths.
