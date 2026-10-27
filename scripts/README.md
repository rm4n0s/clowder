# Scripts

Helper scripts for development. Run them from anywhere; they find the repository themselves.

| Script | What it does |
|---|---|
| `perf.sh [data-dir]` | Measures the targets of SPEC §10 that need a running server (`docs/PERFORMANCE.md`): builds `clowder` and `clowder-perf`, makes the large demo (`clowder demo -size large`) in a temporary directory, or copies the data directory given as the argument, starts `clowder serve` on a free port, logs in, opens 200 streams of the room `general`, posts, and prints the time until every stream received the post (p50, p95, max) and the resident memory of the server with the 200 streams idle. It stops the server and removes its temporary directory when it ends, also on failure or Ctrl-C. `STREAMS` and `ROUNDS` change the number of streams (200) and probe posts (5). Needs `go` and `bc`. |

The helper that talks to the server is the Go program `cmd/clowder-perf` (no dependencies besides the module's own); `clowder-perf -h` lists its flags. The benchmarks of the feed and of search are ordinary Go benchmarks that skip unless `CLOWDER_BENCH_DB` names the database file of a large demo (see `docs/PERFORMANCE.md`).
