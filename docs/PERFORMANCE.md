# Performance

Step 13.1 measured the targets of SPEC §10 on the large demo data set and fixed what missed. This file records the machine, the commands, the numbers and the test-suite times. Numbers are from 2026-10-27.

## Machine

| | |
|---|---|
| Kernel | `Linux fedora 7.3.0-363.vanilla.fc44.x86_64 #1 SMP PREEMPT_DYNAMIC Mon Oct 19 00:08:48 UTC 2026 x86_64 GNU/Linux` |
| CPU | AMD RYZEN AI MAX+ 395 w/ Radeon 8060S (`lscpu` model name) |
| Logical CPUs | 32 (`nproc`) |
| Memory | 30 GiB |
| Go | `go version go1.27.3 linux/amd64` |

The server, the load generator and the database run on the same machine, with the database on the local disk. A real deployment with browsers on a network adds the network time to the stream numbers.

## The data set

`clowder demo -size large` (SPEC §10, D35) adds this to the small demo. All counts are printed by the command:

| | |
|---|---|
| Users | 200 |
| Rooms | 100 (14 private), 4,957 memberships, 300 board columns |
| Posts and comments | 100,000 (69,977 posts, 30,023 comments in threads of varied depth) |
| Tags | 48, 68,154 links to posts |
| Reactions, mentions, attachments | 30,325, 8,233, 415 (with files on disk) |
| Tasks | 20,000 (17,425 assignments), spread over the boards |
| Conversations | 13 with 446 messages |
| Audit events | 124,962 |
| Full-text index | 99,999 rows (every post and comment except the one deleted comment of the small demo) |

Rooms, memberships and conversations go through the services; the rest is written in bulk in one transaction and the full-text index is rebuilt from it. The command checks the result against the rules of the services (index complete, comments in the room of their root, authors are members, tasks in a column of their room, positions without gaps) and fails if one is broken. The data are seeded: a scale gives the same data every time.

**Build time: 7.5 s** (`large demo made in 7.542s`; 7.9 s for the whole `clowder demo -size large` process, 107 MB database).

## Commands to reproduce

```sh
# The data set (an empty directory) and the benchmarks of the feed and search.
go run ./cmd/clowder demo -data /tmp/large -size large
CLOWDER_BENCH_DB=/tmp/large/clowder.db go test ./internal/web ./internal/app -run '^$' -bench Large -benchtime 200x -count 3

# The stream fan-out and the memory with 200 streams: builds everything, makes its own
# large demo (or copies the data directory given as the first argument), starts the
# server on a free port, measures, and stops everything it started.
scripts/perf.sh

# The test suites.
go test -race ./...
```

The benchmarks skip without `CLOWDER_BENCH_DB`, so `go test ./...` does not run them. They work on a copy of the database.

## Results

| What | Target | Measured | |
|---|---|---|---|
| A room's feed page (30 posts), whole `GET /rooms/{id}` handler | under 50 ms | 2.8–3.4 ms (busiest room, 12,283 posts and comments); 6.9–7.1 ms (`general`, 200 members) | ✅ |
| A search with words and two tags (`review deploy #release #bug`) | under 100 ms | 31–32 ms (handler), 28–30 ms (`Search.Query`) | ✅ |
| The same with a very common word (`the #docs #backend`) | under 100 ms | 66–72 ms (handler and service) | ✅ |
| A new post reaching 200 open streams of its room | under 200 ms | p50 20.0 ms, p95 25.2 ms, max 25.9 ms (5 posts × 200 streams) | ✅ |
| Memory of an idle server with 200 open streams | under 150 MB | 110.2 MiB (26.6 MiB before the streams) | ✅ |

More search numbers, from the same run: two words (`release plan`) 41–47 ms; a tag alone (`#design`) 0.7–1.6 ms.

Notes:

- **Feed.** The benchmark is the whole handler through `httptest` (session, queries, markdown, templ), without the network. The busiest room is the one with the most posts and comments; the feed is an indexed query by room, so the size of the room barely matters (the average room has 1,000 posts and comments). `general` is slower because its page also lists 200 members.
- **Fan-out.** One user logs in, opens 200 streams of the room the way the room page does (with `seen=<newest post>`), and posts; the time counts from sending the post to the arrival of its card in each stream. Each of the 200 streams reads the post again for its user and renders it. The post itself is answered in 13–20 ms.
- **Memory.** The resident memory (`VmRSS` in `/proc/<pid>/status`) after the 200 streams have rested for 2 s. A heap profile shows about 25 MiB in use in the Go heap; the rest is outside it, most likely in the SQLite connections (the pure-Go driver allocates its page caches and parsed schemas itself, and the pool keeps 32 connections open). The peak over the whole run (`VmHWM`) was 200 MiB; it comes from the 200 simultaneous renderings of a new post. The target is about the idle server, which is what is measured; a deployment that must stay under a hard limit should leave room for the peak.
- The numbers vary by a few percent from run to run; the ranges above are from three runs.

## What was fixed

Two things missed or came close; both fixes have tests.

1. **Connection pool (D34).** The first run of `scripts/perf.sh` gave a fan-out of p50 268 ms, max 369 ms (target 200 ms) and 113–145 MiB of memory over two runs. A CPU profile of the server during the fan-out showed 53% of its time in `database/sql` opening connections: `database/sql` keeps two idle connections, so 200 streams reading at once opened up to 200 SQLite connections, closed all but two when they finished, and opened them again for the next post; every new SQLite connection parses the whole schema. `store.Open` now bounds the pool of a file database to two connections per CPU, at least 8 and at most 32, and keeps them all open (`poolSize`; tests in `internal/store/pool_test.go`). The fan-out fell to max 25.9 ms, and the memory is bounded by the pool. The in-memory test databases keep their single connection. The rule that follows, now in D34: no code may hold a connection and ask for another one (a transaction uses the `*Store` it is given). The suite passes with the limit, and the in-memory tests already ran on a single connection.
2. **Search with tags.** The tag filter counted a join of `post_tags` and `tags` for every row the full-text index matched. A search for a very common word with two tags (87,000 matches) took 138 ms in the handler, over the target. It is now one `EXISTS` probe of the unique index (post, tag) per tag, with the tag ID looked up once. The same searches take 62–70 ms, and the usual one 28–32 ms (from 45 ms). The existing search tests cover the semantics (all tags required, an unknown tag matches nothing).

The query plans are otherwise as they should be: the feed uses `idx_posts_feed`, the search scans the full-text index and probes the others by primary key. A word that almost every post has (here "the") makes the full-text scan itself (6 ms for 87,000 rows in the `sqlite3` shell) and the ranking the larger part; a search ranked by relevance has to look at every match.

## Test suites

`go test -race`, the packages alone on this machine (`-count=1`):

| Package | Before | After | Cause |
|---|---|---|---|
| `internal/app` | 179 s | 31 s | see below |
| `internal/web` | 165 s | 29 s | see below |

("Before" is the first measurement of this step, with the two packages running together as in `go test ./...`; run alone they took about 50 s and 37 s after only adding `t.Parallel`.) The whole `go test -race -count=1 ./...` takes 39 s (all packages run at once); in it `internal/app` takes 32 s, `internal/web` 31 s, `internal/store` 33 s and `cmd/clowder` 31 s.

Causes, found with `go test -race -json` sorted by elapsed time and a CPU profile of one test:

- **Opening a store per test.** Every test made a new in-memory store, and `store.Open` migrates it: GORM's `AutoMigrate` of 21 models and the raw migrations. Under the race detector that is 90 ms per store, 41% of the CPU of the package. Passwords were already hashed with the fast hasher. `storetest.Open` now migrates one database per test binary (`store.NewTemplate`) and gives each test a copy of the file (`Template.Open`), 4 ms. The copy is a file in the test's temporary directory, so the tests now run on the same kind of database as the program (WAL, the bounded pool), not on a shared-cache in-memory one.
- **No test ran in parallel.** The 32 cores were idle: 6.7 cores busy on average. `t.Parallel()` is now the first line of the 597 top-level tests of `internal/app` and `internal/web` (they all make their own database, and none sets an environment variable or shares state).
- **Timing.** The stream tests wait `quietTime` to see that nothing more comes. With the tests in parallel and the race detector on, 150 ms was sometimes too short for a stream to read and render an event (`TestPageStreamsKeepTheMessageCountLive` failed once in three shuffled runs). It is 400 ms now; three shuffled runs of both packages (`-count=3 -shuffle=on`) pass.

The production hashing parameters are untouched, and `internal/auth` still tests them (`DefaultHasher`).
