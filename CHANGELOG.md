# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-10-17

First public release.

### Added

- TUI to review the plan by category, toggle items, see each item's history,
  and apply after confirmation.
- Scans the top level of Downloads, Desktop, Documents and the home folder.
  Folders move as a unit.
- Rules that classify by Spotlight metadata first and extension second.
- Optional decision model through TypeSafe's System One API (Jev by default).
  The API key is stored in the macOS Keychain. With `-local`, a private
  Ollaya instance runs instead.
- Layout into macOS's own folders, with projects going to an auto-detected,
  remembered workspace and sorted into `web/`, `blockchain/`, `mobil/`,
  `backend/`, `araclar/` and `diger/`.
- Unpacking of project piles, including piles left inside the workspace.
- Deletion of regenerable build folders in stale projects, with manifest and
  `git check-ignore` checks.
- Removal of empty folders.
- Moves with `RENAME_EXCL` that never overwrite. Every change is recorded in
  a per-file event chain in SQLite, and `-undo` reverts the last apply.
- Summary of moved items, files, freed space and elapsed time.
- Protection for system, toolchain and app-owned folders, Chrome profiles and
  undownloaded iCloud files.

[Unreleased]: https://github.com/batikankutluer/mac-cleaner/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/batikankutluer/mac-cleaner/releases/tag/v0.1.0
