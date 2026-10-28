# Scripts

Helper scripts for development. Run them from anywhere; they find the repository themselves.

| Script | What it does |
|---|---|
| `perf.sh [data-dir]` | Measures the targets of SPEC §10 that need a running server (`docs/PERFORMANCE.md`): builds `clowder` and `clowder-perf`, makes the large demo (`clowder demo -size large`) in a temporary directory, or copies the data directory given as the argument, starts `clowder serve` on a free port, logs in, opens 200 streams of the room `general`, posts, and prints the time until every stream received the post (p50, p95, max) and the resident memory of the server with the 200 streams idle. It stops the server and removes its temporary directory when it ends, also on failure or Ctrl-C. `STREAMS` and `ROUNDS` change the number of streams (200) and probe posts (5). Needs `go` and `bc`. On PostgreSQL (step 17.1): `DATABASE_URL` is the URL of an empty database for the demo (with a data directory as the argument, of a database that already has it), `REDIS_URL` and `PROCESSES=2` start a second process on the same database and directory and open 100 streams on each, writing the posts on the first; `SETTLE` is the rest before the memory is read. |
| `perf-postgres.sh` | Runs the whole PostgreSQL measurement of SPEC §10 on throw-away `postgres:18` and `redis:8` containers: the large demo on PostgreSQL, the benchmarks of the feed and of the search (`CLOWDER_BENCH_DATABASE_URL`), `perf.sh` with one process and with two processes and Redis. `SKIP_CLUSTER=1` and `ONLY_CLUSTER=1` split it in two. About 6 minutes; its output is `docs/implemented-proof-images/17.1-perf-postgres.txt` and `17.1-perf-cluster.txt`. Needs `docker` (or `podman`), `go` and `bc`. |
| `test-docker.sh [go test args]` | Runs the test suite against throw-away `postgres:18` and `redis:8` containers (SPEC D39) and removes them. |
| `compose-smoke.sh` | Smoke test of `compose.yaml` (SPEC D40) on a throw-away compose project: builds the image, starts Clowder and PostgreSQL, checks both are healthy, makes an admin invitation, signs up through the form with `curl`, opens the home page logged in, asks `/healthz`, and removes the containers and volumes. Needs `docker` and `curl`. |

The helper that talks to the server is the Go program `cmd/clowder-perf` (no dependencies besides the module's own); `clowder-perf -h` lists its flags. The benchmarks of the feed and of search are ordinary Go benchmarks that skip unless `CLOWDER_BENCH_DB` names the database file of a large demo (see `docs/PERFORMANCE.md`; with `CLOWDER_BENCH_DATABASE_URL` set to the URL of a large demo on PostgreSQL they run on it).

## Rendering the icons

The PNG icons are rendered once from an SVG with headless Chromium, by `clowder-proof` (build tag `clowder_browser`; it needs Chromium on `PATH`, no server and no data). The PNGs are committed; run these again only when the SVG changes (from the repository root):

```sh
# the favicon fallback: 32 px, transparent ground
go run -tags clowder_browser ./cmd/clowder-proof -render-icon internal/web/static/favicon.svg -icon-size 32 -o internal/web/static/favicon-32.png

# the icon iOS puts on the home screen: 180 px on the cream ground, with 20% around the head
go run -tags clowder_browser ./cmd/clowder-proof -render-icon internal/web/static/favicon.svg -icon-size 180 -icon-bg '#fbf6ee' -icon-pad 20 -o internal/web/static/apple-touch-icon.png
```

`-icon-size` is the width and the height in pixels (16 to 1024), `-icon-bg` is a `#rrggbb` ground (transparent when left out) and `-icon-pad` the space around the icon on each side, in percent (0 to 40). The icons of the installed app (step 21.5, `internal/web/static/manifest.webmanifest`) are rendered the same way. The "any" icons are the favicon on a transparent ground; the maskable ones are `icon-maskable.svg` (the cat head in the cream of the header, with the accent for the eyes and the nose) on the accent ground with 15% around it, so that the head stays inside the safe zone of a mask:

```sh
for size in 192 512; do
  # purpose "any": the head in the accent, transparent ground
  go run -tags clowder_browser ./cmd/clowder-proof -render-icon internal/web/static/favicon.svg -icon-size $size -o internal/web/static/icon-$size.png
  # purpose "maskable": the cream head on the accent ground
  go run -tags clowder_browser ./cmd/clowder-proof -render-icon internal/web/static/icon-maskable.svg -icon-size $size -icon-bg '#c2410c' -icon-pad 15 -o internal/web/static/icon-maskable-$size.png
done
```

`icon-maskable.svg` is not embedded in the binary (it is only the source of the PNGs).
