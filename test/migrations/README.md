# Migration fixtures

Every stable `v1.*.*` and `v2.*.*` release is booted for real, seeded via API, and captured as fixture. We then migrate... all of them to LTS. 

## Running

```sh
make fixtures                       # every tag, skips fixtures that exist
make fixtures FIXTURE_ARGS=-force   # recapture everything
cd test/migrations && go run ./fixturegen -tags v2.0.15 -keep-work
go test ./test/migrations
```

`go generate ./...` and therefore `make gen` also run the generator
