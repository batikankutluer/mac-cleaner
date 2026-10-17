# mac-cleaner

A terminal UI that tidies a Mac: it looks at the loose files and folders in
your Downloads, Desktop, Documents and home folder, decides where each one
belongs from its metadata, and moves it there once you approve the plan.
Every move is recorded and can be undone.

> The interface is in Turkish. Kategoriler ve arayüz Türkçedir.

```
mac-cleaner  ·  64 öğe · 2.1 GB · 60 seçili · 1.4 GB temizlenecek · …
▸ Tümü 64         [x] old-dapp/             → ~/code/blockchain       12.4 MB  ✂
  Projeler 9      [x] landing-page/         → ~/code/web             480.0 KB
  Belgeler 18     [x] rapor.pdf             → ~/Documents/Belgeler     1.2 MB
  Videolar 11     [x] klip.mp4              → ~/Movies                30.0 MB
```

## What it does

- **Files** go to macOS's own folders: videos to `~/Movies`, images to
  `~/Pictures`, audio to `~/Music`, screenshots to `~/Pictures/Ekran
  Görüntüleri`, installers to `~/Downloads/Kurulumlar`, everything else to
  `~/Documents/<category>`.
- **Folders** move as a unit. A folder with a `.git`, `go.mod`,
  `package.json`, `foundry.toml`, `compose.yml`, … (or with such projects
  inside it, or mostly code) is a **project** and goes to your workspace.
- **The workspace is found automatically**: the folder under your home with
  the most projects in it (for example `~/code`). It is remembered;
  `-workspace` changes it.
- **Projects are grouped** inside the workspace by stack: `web/`,
  `blockchain/`, `mobil/`, `backend/`, `araclar/` (small scripts), `diger/`.
- **Piles are unpacked**: folders named like `Projects`, `repos` or
  `old-projects` are opened and each thing inside is sorted on its own.
  `myapp/frontend` and `myapp/backend` stay together as `myapp`.
- **Stale build output is deleted**: in projects untouched for 90 days
  (`-stale-days`), regenerable folders like `node_modules`, `.next`, `target`,
  `.venv`, foundry `out/` are removed before the move.
- **Empty folders are removed.**
- **At the end** it reports what it moved, how many files, how much disk space
  it freed and how long it took.

## Safety

- Nothing happens until you press `u` and confirm with `e`.
- A move is a single `rename(2)` with `RENAME_EXCL`: it never overwrites
  anything, and tags, extended attributes and dates survive.
- Before each move the item's identity (inode) and, for files, size and mtime
  are checked again. Anything that changed since the scan is skipped.
- Build folders are deleted only when the manifest that regenerates them sits
  next to them (`package.json` for `node_modules`, `Cargo.toml` for `target`,
  …) and, inside a git repository, only when git ignores them. `dist/` and
  `build/` are never touched; some repositories commit them.
- Empty folders are removed with `rmdir(2)`, which refuses a folder with
  anything in it. The only file ever deleted besides build output is
  Finder's `.DS_Store`.
- Left alone: `~/Library`, `~/Applications`, `~/go` (GOPATH), `~/bin`,
  anything on `PATH`, folders apps keep for themselves (named after an
  installed app or a known vendor, notes vaults, browser profiles), and iCloud
  files that are not downloaded.
- Workspace projects changed in the last 7 days are offered but not selected:
  they are probably open in an editor.
- `cleaner -undo` moves everything back and recreates removed empty folders.
  Deleted build folders do not come back; `npm install` and friends rebuild
  them.

## Install

Requires macOS 14 or later.

```sh
go install github.com/batikankutluer/mac-cleaner@latest   # installs as mac-cleaner
```

or download a binary from the
[releases](https://github.com/batikankutluer/mac-cleaner/releases), or build
from source:

```sh
git clone https://github.com/batikankutluer/mac-cleaner
cd mac-cleaner
make install    # installs as `cleaner` into ~/.local/bin
```

## Usage

```sh
cleaner                  # scan, review in the TUI, apply
cleaner ~/some/folder    # scan other folders instead
cleaner -print           # print the plan, no TUI, nothing moved
cleaner -undo            # revert the last apply
cleaner -version
```

Keys in the TUI:

| Key | Action |
|---|---|
| `↑` `↓` / `j` `k` | move |
| `←` `→` / `tab` | switch category |
| `space` | select or deselect the item |
| `a` | select or deselect the whole category |
| `c` | turn build-folder cleaning on or off for the item |
| `enter` | the item's history |
| `u` | apply (asks for confirmation) |
| `s` | save the plan without applying |
| `q` | quit |

Flags:

| Flag | Default | |
|---|---|---|
| `-workspace` | detected | where projects go; remembered once given |
| `-stale-days` | `90` | delete build folders of projects untouched this long; `0` turns it off |
| `-rules-only` | | decide by rules alone, no model |
| `-model` | `jev-1.14.0` | decision model |
| `-model-url` | TypeSafe | another System One compatible server |
| `-local` | | run a local model through a private Ollaya instance instead |
| `-reset-key` | | forget the stored TypeSafe API key |
| `-min-confidence` | `0.5` | below this, the model's answer is ignored |
| `-workers` | `8` | parallel model requests |
| `-db` | `~/Library/Application Support/mac-cleaner/state.db` | history database |

## How decisions are made

Rules come first and are final when they rest on metadata: Spotlight's content
type (`kMDItemContentType` and its conformance tree), the screen-capture flag,
macOS's screenshot names, project markers. A model is asked only when the
rules are unsure: a file known only by its extension, or nothing matching at
all. Even then it must be more confident than the rules to win.

The model is a structured decision model behind TypeSafe's System One API
(`POST /v1/systemone`), [Jev](https://typesafe.ai) by default. The API key is
asked for once in the TUI and kept in the macOS Keychain (`TYPESAFE_API_KEY`
works too). Only metadata is sent, and only for files the rules could not
place: name, extension, size, content type and the host it was downloaded
from. File contents are never read or sent.

`-local` runs an open model instead (default `laya:multilingual`) through a
private copy of [Ollaya](https://github.com/ollaya-dev/ollaya) that is
downloaded once, verified by sha256, bound to `127.0.0.1` and stopped on exit.
Apple Silicon only.

## History

Everything lives in one SQLite file. Each file or folder has an identity
(device and inode) and a chain of events (`seen`, `changed`, `moved`,
`cleaned`, `removed`, `undone`). Each event points to the previous one and
records the old and the new state, so the full history of anything the tool
has touched can be walked back. `enter` in the TUI shows it.

## License

[MIT](LICENSE)
