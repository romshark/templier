---
name: changelog
description: Write CHANGELOG.md entries, including Security entries, and release a version. Use when adding a changelog entry or releasing.
---

`CHANGELOG.md` follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). The release workflow publishes each tag's section and fails when it's missing. `TestChangelogReleases` validates version headings.

- Add user-visible CLI, configuration and `engine` package changes under `## [Unreleased]` in the same commit. Omit `docs`, `test`, `chore` and `ci`.
- Sort entries into `Added`, `Changed`, `Deprecated`, `Removed`, `Fixed` and `Security`.
- Use imperative entries like commit titles: `Pass HEAD requests through the proxy`. Say enough for users to determine whether they are affected.
- A `Security` entry follows [Security entries](#security-entries).
- A breaking change says what users have to change.
- To release, rename `## [Unreleased]` to `## [X.Y.Z] - YYYY-MM-DD`, insert an empty `## [Unreleased]`, update the compare links and tag `vX.Y.Z`.

# Security entries

A security entry lets users determine exposure and remediation:

- State who can read or do what to whom, as in "any website open in the browser can …", not "a security issue".
- Describe affected setups with terms users can find in their configuration.
- Give exact affected versions, verified against the release history.
- Give upgrade steps. Include a workaround or say there is none.
- Link the GHSA or CVE when one exists.

Leave severity labels, CVSS scores and exploit steps to the advisory.
