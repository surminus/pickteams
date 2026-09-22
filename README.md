# Pick Teams

Picks two even sides for a game of football based upon weightings of the
players present.

## Quick Start

```
export PICKTEAMS_ADMIN_PASSWORD='something long'
go run . -addr 127.0.0.1:8080 -db pickteams.db
```

It will not start without a password. The database and the cookie signing key
are created on first run, so restarts do not sign you out.

`make build` gives one binary with the templates, CSS and htmx embedded.
`make check` runs vet and the tests. `pickteams -version` prints the version
the binary was built from.

## Releases

Built binaries for Linux and macOS, amd64 and arm64, are on the
[releases page](https://github.com/surminus/pickteams/releases). Each one is a
tarball with the binary, the deploy files and the licence, and `SHA256SUMS`
covers the lot.

Download, check, install:

```
sha256sum --ignore-missing -c SHA256SUMS
tar -xzf pickteams_v1.0.0_linux_amd64.tar.gz
sudo install -m 0755 pickteams_v1.0.0_linux_amd64/pickteams /usr/local/bin/pickteams
```

To cut a release, tag a commit on `main` and push the tag:

```
git tag -a v1.0.0 -m 'v1.0.0'
git push origin v1.0.0
```

The `release` workflow runs the checks, cross-compiles everything and creates
the GitHub release with generated notes. `make dist` does the same build
locally if you want to see what it will produce.

## Licence

MIT. See `LICENSE`.
