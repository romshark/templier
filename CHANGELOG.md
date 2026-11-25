# Changelog

This file records the notable changes of each release in the format of
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). The release
workflow publishes the section of a version as its GitHub release notes.
Releases up to v0.13.5 have their notes on
[GitHub Releases](https://github.com/romshark/templier/releases) only.

## [Unreleased]

### Changed

- Show warnings at the default log level `erronly`, such as for an unsupported templ version or a custom watcher command that isn't installed.

### Fixed

- Remove the temporary `go-build` directories of canceled builds instead of leaving them in the OS temp directory ([#25](https://github.com/romshark/templier/issues/25)).
- Stop hanging when templier is stopped within a custom watcher's `debounce` after a file change.
- Run a custom watcher once for all file changes within its `debounce`, as documented, instead of once per change.
- Rebuild for files in `app.exclude` when a custom watcher with `requires: rebuild` matches them.
- Make the proxy retry only until `proxy-timeout` passes, and only when the app server doesn't accept connections. Requests retried for up to 11 minutes, and a request the app server had already received could run twice.
- Remove app server binaries that are never run instead of keeping them until templier exits.
- Shut down cleanly on SIGTERM instead of leaving the app server and `templ generate --watch` running. On Windows, this also covers closing the console window.
- Skip directories matched by `watcher-ignore` entirely. An unreadable directory or a file deleted during startup no longer stops templier from starting.
- Treat any non-zero exit of a custom watcher's `cmd` as a failure, not only exit code 1: skip the action it requires and, with `fail-on-error`, show its output in the browser.
- Show the output of `go build` and `golangci-lint` when they fail with an exit code other than 1.
- Stop concurrent page responses from swapping their `</head>` and `</body>` tags.
- Close browser connections when `engine.Run` returns.
- Pass HEAD requests through the proxy. Compressed pages returned 502 and others `Content-Length: 0`.
- Stop logging an error after successfully formatting a `.templ` file with `format: true`.
- Show the error page when the app server crashes after it has started. Pages returned 502 and nothing was logged.
- Resolve a relative `app.dir-cmd` against templier's working directory, like the other paths. With an `app.dir-work` other than `./`, it was rejected or failed to build.

[Unreleased]: https://github.com/romshark/templier/compare/v0.13.5...HEAD
