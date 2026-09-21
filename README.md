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
`make check` runs vet and the tests.

## Licence

MIT. See `LICENSE`.
