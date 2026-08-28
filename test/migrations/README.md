# Migration fixtures

We test migrations from the last release of the previos version. In the case of v3, we test v2.0.15 onwards (inclusive).

## Running

```sh
make fixtures                       # every bootable release, skips fixtures that exist
make fixtures FIXTURE_ARGS=-force   # recapture everything
cd test/migrations && go run ./fixturegen -tags v2.0.15 -keep-work
go test ./test/migrations/...
```

`go generate ./...` and therefore `make gen` also run the generator
