# mcprotogen

Derives the hub codec's per protocol wire tables from official Minecraft jars.

## Install

```sh
go build -o mcprotogen ./scripts/mcprotogen
```

Needs a `java` on the path new enough for the newest requested release.

## Usage

```sh
mcprotogen gen --since 1.20.5        # regenerate pkg/mcproto/mcdata/data for every release since
mcprotogen gen 26.4                  # add one release
mcprotogen diff 26.3 26.4            # list hub relevant classes whose bytecode changed
mcprotogen verify 26.4               # join the hub codec with the official client
mcprotogen serve --port 25599        # host the hub codec for any client you point at it
```
