---
title: Contributing
description: Setting up a development environment, the protobuf workflow, tests, and what pull requests should look like.
---

DiscoPanel lives at [github.com/discohaus/discopanel](https://github.com/discohaus/discopanel). Issues and pull requests are welcome. For questions, the [Discord](https://discord.gg/6Z9yKTbsrP) is faster.

## Toolchain

- **Go** 1.24+
- **Node.js** 22+
- **Docker** - needed at build time too: protobuf generation runs [buf](https://buf.build/) in a container, and the tests that touch containers obviously need an engine.
- **Make**

## First build

```sh
git clone https://github.com/discohaus/discopanel.git
cd discopanel
make gen      # generate Go + TypeScript from the protos
make deps     # go mod download + npm install
make dev      # backend (go run) + frontend (vite dev) together
```

`make dev` also resets the local database from `dev/discopanel.db` (a seeded dev state). Use `make run` to keep your current data. The backend listens on 8080, the Vite dev server proxies to it with hot reload.

## The protobuf workflow

The `.proto` files in `proto/discopanel/v1/` are the source of truth for the entire API. The generated code lands in `pkg/proto` (Go) and `web/discopanel/src/lib/proto` (TypeScript) and is never edited by hand.

- After changing a proto, run `make gen` (cleans and regenerates both sides).
- `make proto-lint` and `make proto-format` keep the definitions tidy.
- `make proto-breaking` checks your branch against `main` for breaking API changes.

If you're adding a per-server setting rather than an RPC, you may not need to touch the protos at all - see [the config field pipeline](/development/api-and-data/#adding-a-per-server-setting).

## Tests

```sh
make test     # go test ./...
make lint     # buf lint + frontend eslint/prettier
make check    # frontend type checking (svelte-check)
```

The end-to-end test boots a real Minecraft server through the full stack (provisioner, container, health, RCON, graceful stop). Run it before releases and whenever you touch the provisioner, the docker client, the runtime entrypoint or the health code:

```sh
make runtime-local-21    # once, to have a local runtime image
DISCO_E2E=1 go test ./internal/lifecycle -run TestE2EVanillaServer -v -timeout 20m
```

Details and knobs in [Lifecycle & Health](/development/lifecycle/#the-end-to-end-test).

## Building images

- `make image` - the panel image.
- `make runtime-local-<N>` - one runtime variant locally, no push (e.g. `make runtime-local-21`).
- `make runtime` - every runtime variant, pushed.
- `make modules` - runtime plus all sidecar module images.
- CI builds multi-arch (amd64 + arm64) when a `mod-*` git tag is pushed or the Module Builder workflow is dispatched.

## What pull requests should look like

- **Complete features.** No TODOs, no placeholders, no "wire this up later". If a change spans backend and frontend, ship both halves.
- **One implementation per concept.** Before adding a structure, a helper or an event type, read how the existing code handles the same concern and extend that instead. Parallel implementations of the same idea get rolled back.
- **Respect the ownership boundaries.** Server state transitions go through `lifecycle.Manager`, events through `events.Bus`, container work through `internal/docker`. Don't reach around them.
- **Regenerate, don't hand-edit.** If your diff touches `pkg/proto` or `web/discopanel/src/lib/proto` without a matching proto change, something went wrong.
- Target `main`, keep the diff focused, and say in the description what you tested (unit tests, e2e, or a manual run).
