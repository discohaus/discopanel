---
title: Contributing
description: How the project is put together, the development workflow, and what pull requests should look like.
---

DiscoPanel lives at [github.com/discohaus/discopanel](https://github.com/discohaus/discopanel). Issues and pull requests are welcome. For questions, the [Discord](https://discord.gg/6Z9yKTbsrP) is faster.

## How the project is put together

The panel is one Go process: it embeds the SvelteKit web UI, keeps its state in SQLite, and talks straight to the Docker Engine API. Everything you interact with - the API, the provisioner, the proxy, the scheduler, the module orchestration - is in this repository.

Two pieces ship as container images built from private source:

- **discoruntime** (`ghcr.io/discohaus/discoruntime`) - the minimal image every Minecraft server runs on. The panel installs server software into the data directory, and the container just supervises Java.
- **discomodule** (`ghcr.io/discohaus/discomodule-*`) - the built-in module images (Geyser, Steam Bridge, Playit.gg, Doctor, and friends).

The contracts between the panel and those images are public and live here, so panel changes never chase private code:

- `proto/discopanel/agent/v1` - the telemetry stream between a running server container and the panel.
- `proto/discopanel/v1/runtime.proto` and `pkg/runtimespec` - the file contract (`launch.json`, `agent.json`, the manifest) the panel writes into each server's `.discopanel/` folder.
- `proto/discopanel/v1/doctor.proto` - the shared journal contract for the Doctor module.
- `pkg/mcproto` and `pkg/moduleprompt` - helper libraries for anyone building their own module.

Panel code never imports the private repos - `make check` fails the build if it does.

## Toolchain

- **Go** 1.25+
- **Node.js** 22+
- **Docker** - protobuf generation runs [buf](https://buf.build/) in a container, and the tests that touch containers need an engine.
- **Make**

A Nix dev shell with all of the above is available via `nix develop`.

## First build

```sh
git clone --recurse-submodules https://github.com/discohaus/discopanel.git
cd discopanel
make gen      # generate code from the protos
make deps     # go mod download + npm install
make dev      # backend (go run) + frontend (vite dev) together
```

`make dev` also resets the local database from `dev/discopanel.db` (a seeded dev state). Use `make run` to keep your current data. The backend listens on 8080, the Vite dev server proxies to it with hot reload.

## The protobuf workflow

The `.proto` files in `proto/discopanel/` are the source of truth for the entire API and every persisted data model - `storage.proto` holds all of the latter. `make gen` regenerates everything: the Go server code (`pkg/proto`), the TypeScript clients (`web/discopanel/src/lib/proto`), and the GORM storage layer (protogorm generates `internal/db/store.gen.go` from the same protos). None of it is ever edited by hand.

- After changing a proto, run `make gen` (cleans and regenerates all of it).
- `make proto-lint` and `make proto-format` keep the definitions tidy.
- `make proto-breaking` checks your branch against `main` for breaking API changes.

## Tests and checks

```sh
make test     # go test ./...
make lint     # buf lint + frontend eslint/prettier
make check    # svelte-check plus the private-import guard
```

## What pull requests should look like

- **Complete features.** No TODOs, no placeholders, no "wire this up later". If a change spans backend and frontend, ship both halves.
- **One implementation per concept.** Before adding a structure, a helper or an event type, read how the existing code handles the same concern and extend that instead. Parallel implementations of the same idea get rolled back.
- **Respect the ownership boundaries.** Server state transitions go through `lifecycle.Manager`, events through the `pkg/events` bus, container work through `internal/docker`. Don't reach around them.
- **Regenerate, don't hand-edit.** If your diff touches generated code without a matching proto change, something went wrong.
- Target `main`, keep the diff focused, and say in the description what you tested.

## Docs

This documentation site lives in `docs/discopanel/` (Astro + Starlight). `make dev-docs` serves it locally on `http://localhost:4321`. If something here is wrong or missing, open an issue or mention it in Discord.

The UI screenshots are captured by `docs/discopanel/scripts/screenshots.mjs` against a running panel. Point it at one with a server or two and it refreshes every image in `src/assets/screenshots/`:

```sh
cd docs/discopanel
PANEL_URL=http://localhost:8080 PANEL_USER=admin PANEL_PASS=... node scripts/screenshots.mjs
```
