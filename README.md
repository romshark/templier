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
that behaves similarly to templ's native
[`--watch`](https://templ.guide/developer-tools/live-reload/) mode but provides
more functionality and reports all errors directly to all open browser tabs ✨.

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

Then copy-paste [example-config.yml](https://github.com/romshark/templier/blob/main/example-config.yml) to your project source folder as `templier.yml`, edit to your needs and run:

```sh
templier --config ./templier.yml
```

ℹ️ Templiér automatically detects `templier.yml` and `templier.yaml` in the directory it's running in without the explicit `--config` flag.

## How is Templiér different from templ's own watch mode?

As you may already know, templ supports [live reload](https://templ.guide/commands-and-tools/live-reload)
out of the box using `templ generate --watch --proxy="http://localhost:8080" --cmd="go run ."`,
which is great, but Templiér provides even better developer experience:

- 🥶 Templiér doesn't become unresponsive when the Go code fails to compile,
  instead it prints the compiler error output to the browser tab with ANSI
  colors preserved and keeps watching.
  Once you fixed the Go code, Templiér will reload and work as usual with no intervention.
  In contrast, templ's watcher needs to be restarted manually.
- 📁 Templiér watches **all** file changes recursively,
  recompiles and restarts the server
  (unless the file matches `app.exclude` or is handled by a [custom watcher](#custom-watchers)).
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
- name: Build CSS
  cmd: npm run build:css
  include: ["*.templ", "input.css"]
  exclude: ["path/to/your/dist.css"]
  fail-on-error: true
  debounce:
  requires: reload
```

NOTE: if your `dist.css` is embedded, you may need to use `requires: rebuild`.

### Custom Watcher Example: Reload on config change.

Normally, Templiér rebuilds and restarts the server when any file changes (except for
`.templ` files). However, when a config file changes we don't usually
require rebuilding the server. Restarting the server may be sufficient in this case:

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
saving it in the OS' temp directory (cleaned up latest before exiting) assuming that
the main package is specified by the `app.dir-cmd` directory.
Custom Go compiler arguments can be specified with `compiler`. Once built, the application server
executable is launched with `app.flags` CLI parameters and the working directory
set to `app.dir-work`. When necessary, the application server process is shut down
gracefully, rebuilt, linted and restarted.
On `.templ` file changes Templiér only tries to compile and lint the server code
without refreshing the page.

Templiér hosts your application under the URL specified by `templier-host` and proxies
all requests to the application server process that it launched injecting Templiér
JavaScript that opens a websocket connection to Templiér from the browser tab to listen
for events and reload or display necessary status information when necessary.
In the CLI console logs, all Templiér logs are prefixed with 🤖,
while application server logs are displayed without the prefix.

## Development

Run the tests using `go test -race ./...` and use the latest version of
[golangci-lint](https://golangci-lint.run/) to ensure code integrity.

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

### Important Considerations

- Templiér currently doesn't support Windows.
- When measuring performance, make sure you're not running against the Templiér proxy
  that injects the JavaScript for auto-reloading because it will be slower and should
  only be used for development. Instead, use the direct host address of your application
  server specified by `app.host` in your `templier.yml` configuration.
- Templiér's JavaScript injection uses the `GET /__templier/events` HTTP endpoint for
  its websocket connection. Make sure it doesn't collide with your application's paths.
