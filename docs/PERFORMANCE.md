# Performance

Step 13.1 measured the targets of SPEC §10 on the large demo data set on SQLite and fixed what missed; step 17.1 measured them on PostgreSQL, and the fan-out across two processes with Redis (the last part of this file). This file records the machine, the commands, the numbers and the test-suite times. Numbers are from 2026-10-27.

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

---

# PostgreSQL and several processes (step 17.1)

SPEC §10 says that from 1.1 the same targets are measured on PostgreSQL too, and the fan-out also across two processes with Redis. The raw output of the runs is in `docs/implemented-proof-images/17.1-perf-postgres.txt` (the data set, the benchmarks, one process) and `17.1-perf-cluster.txt` (two processes).

## Setup

The machine is the one above (32 logical CPUs, 30 GiB), shared with other work: the load average during the runs was 9 to 13, so single numbers vary. PostgreSQL 18.6 and Redis 8.10 run in the official images (`postgres:18`, `redis:8`) on the same machine, on the loopback address, with the **default configuration of the image** (`shared_buffers` 128 MB, `work_mem` 4 MB, `fsync` and `synchronous_commit` on; only `--shm-size=256m` is added), which is what `docker compose up` runs. The database holds the same data set as the SQLite runs (the generator is seeded): 189 MB on disk against 107 MB for the SQLite file. The server, the load generator, PostgreSQL and Redis share the machine.

## Commands to reproduce

```sh
# Everything below on throw-away containers: the large demo on PostgreSQL, the benchmarks,
# the fan-out and the memory with one process, then with two processes and Redis.
# Needs docker (or podman). About 6 minutes. The output is the proof files.
SKIP_CLUSTER=1 scripts/perf-postgres.sh     # the data set, the benchmarks, one process
ONLY_CLUSTER=1 scripts/perf-postgres.sh     # the two processes

# By hand, against a PostgreSQL you already have (an empty database for the demo):
CLOWDER_DATABASE_URL=postgres://... clowder demo -data /tmp/pgdemo -size large
CLOWDER_BENCH_DATABASE_URL=postgres://... go test ./internal/web ./internal/app -run '^$' -bench Large -benchtime 200x -count 3
DATABASE_URL=postgres://... scripts/perf.sh /tmp/pgdemo                          # one process
DATABASE_URL=postgres://... REDIS_URL=redis://... PROCESSES=2 scripts/perf.sh /tmp/pgdemo   # two processes, 100 streams on each
```

`clowder-perf -base A -base2 B -pid P1 -pid2 P2` is what the script runs: half of the streams are opened on each process, the probe posts are written on the first, and the time counts from sending the post to its arrival in each stream (for the streams of the second process that includes the way through Redis). The memory is the resident memory (`VmRSS`) of each Clowder process, not of the machine.

The large demo was made on PostgreSQL in 12.8 s (13.2 s to 22.6 s in the other runs; 7.5 to 9 s on SQLite), by the same generator, with one `ANALYZE` at the end (below).

## Results

SQLite is the number of step 13.1 (the memory and the fan-out were measured again in this step, in brackets). PostgreSQL is the final run of this step (ranges are the three runs of each benchmark); one process unless it says two.

| What | Target | SQLite (13.1) | PostgreSQL | |
|---|---|---|---|---|
| A room's feed page (30 posts), whole handler, busiest room (12,283 posts and comments) | under 50 ms | 2.8–3.4 ms | 3.2–4.0 ms | ✅ |
| The same, `general` (200 members) | under 50 ms | 6.9–7.1 ms | 5.3–6.4 ms | ✅ |
| A search with words and two tags (`review deploy #release #bug`), handler | under 100 ms | 31–32 ms | 10.6–11.0 ms (`Search.Query`: 10.2–13.9 ms) | ✅ |
| The same with a very common word (`the #docs #backend`) | under 100 ms | 66–72 ms | 4.9–5.1 ms | ✅ |
| Two words (`release plan`), handler | (under 100 ms) | 41–47 ms | 54.6–60.4 ms (`Search.Query`: 57.8–69.2 ms) | ✅ |
| One tag (`#design`), handler | – | 0.7–1.6 ms | 1.7–2.5 ms | ✅ |
| A new post reaching 200 open streams of its room, one process | under 200 ms | p50 20.0, p95 25.2, max 25.9 ms | p50 26–27, max 155 ms and 130 ms in the final run (see below); 37 to 77 ms in the runs before | ✅ |
| The same, 100 streams on each of two processes with Redis, the post written on the first | under 200 ms | – | p50 20–24 ms, p95 30–35 ms, max 31–37 ms; the streams of the other process, through Redis, p50 20–24 ms, max 31–37 ms, the same as those of the process that took the post | ✅ |
| Memory of an idle server with 200 open streams, one process | under 150 MB | 110.2 MiB (105.7 MiB again, after the fix below) | 80.2 MiB after 2 s of rest, 78.6 MiB after 20 s | ✅ |
| Memory of each of two processes with 100 open streams | under 150 MB | – | 70.7 and 70.1 MiB (66.7 and 66.0 MiB after 20 s) | ✅ |

Every target is met. Notes:

- **Feed.** The page reads the posts with an index scan of `posts_pkey` backward (the filter `room_id` and `parent_id IS NULL` removes about 300 rows to find 31): 0.1 ms in `EXPLAIN (ANALYZE)`. The partial index `idx_posts_feed_roots` of migration 0002 is what the planner uses when it has no statistics (next section); with statistics the primary key is cheaper still. The rest of the 3 to 6 ms is the rendering of the page.
- **Search.** The query with two tags starts from the tag (`idx_post_tags_tag_id`, 12,320 and 6,728 rows), joins the other tag, and then probes the primary keys and `posts_search_pkey`: 8,300 buffers, 11 ms. A very common word with two tags is faster than on SQLite because the planner starts from the tags too. A search with only words (`release plan`, 17,000 matches of 100,000) reads the whole `posts_search` table with a parallel sequential scan and ranks every match, because a ranking by relevance has to look at all of them: 55 to 60 ms. The GIN index (`posts_search_tsv_idx`) is used when the words are rare. It is slower than SQLite for this kind of search (41 to 47 ms) and well under the target for a search with tags (words and two tags is the target query).
- **Fan-out.** The numbers of the two outliers (155 ms, 130 ms, and 77 ms in the run before) are rounds in which the post itself was slow to answer (129 ms, 107 ms and 57 ms: the write to PostgreSQL, with `fsync` on, on a machine with a load average above 10), and then every stream is late by the same amount. Rounds without a stall are 20 to 40 ms for one process and for two. The target is met in every run, but a single round on a busy machine can come to three quarters of it; a deployment on a quiet server has the numbers of the quiet rounds.
- **Across processes.** The post written on the first process reaches the streams of the second in the same time as those of the first (p50 20 to 24 ms either way): one Redis publish and one fan-out in the other process add less than the spread between rounds.

## What was found and fixed

1. **A freshly loaded database has no statistics (`Store.Analyze`, D48).** The first run of the benchmarks gave the feed of the busiest room 31 ms in the first two runs and 5 ms in the third. `EXPLAIN (ANALYZE, BUFFERS)` of the feed query right after `clowder demo -size large`, with no statistics, showed a bitmap heap scan of all 8,628 root posts of the room and a sort (3,100 buffers, 6.4 ms); autovacuum analyzed the table about a minute later and the plan became the index scan (18 buffers, 0.1 ms). The large demo is a bulk load, which is the only case where this happens (a database that grows through the services is analyzed by autovacuum as it grows), so `clowder demo -size large` now runs `ANALYZE` once the load is committed. The page took 2.8 to 4.0 ms from the first request after that. Test: `TestAnalyze` (on PostgreSQL it checks `last_analyze`).
2. **A login left 64 MiB in the process (found by the two-process run).** With two processes the first one, which took the login of the measurement, measured 153 to 161 MiB with 100 streams, over the target, while the second, with 100 streams and no login, measured 65 MiB. Doing one login against a server with nothing else to do left its resident memory at 95 MiB instead of 30, for ever (140 s were watched): an Argon2id hash allocates 64 MiB, the Go runtime keeps it, and it does not give it back while little else is allocated. With 200 streams the new allocations reuse that memory, which is why the single process showed 90 to 110 MiB and did not miss. The password-hashing gate (D37) now calls `debug.FreeOSMemory` when the last hash in flight ends, for hashers of 16 MiB or more (`gate.idle`, `Hasher.MemoryBytes`): the same login leaves the server at 32.6 MiB, and the processes of the runs above measure 70 MiB with 100 streams. A garbage collection after a burst of logins costs a few milliseconds next to the hash itself. Tests: `TestGateCallsIdleWhenTheLastCallerLeaves`, `TestOnlyTheRealHasherGivesMemoryBack`, `TestMemoryBytes`. On SQLite the memory with 200 streams is 105.7 MiB (110.2 before), most of it in the pool of SQLite connections as in 13.1.

## What could not be measured, and what is not measured

- The runs are on one machine that is shared with other work (a load average of 9 to 13), with the server, the load generator, PostgreSQL and Redis together. A real deployment with the database on another machine adds the network time to the write and the read of every request, and PostgreSQL on another machine does not compete with Clowder for the CPUs.
- The default configuration of PostgreSQL was used. `shared_buffers` of 128 MB is below the 189 MB database; the operating system's cache holds the rest here. A deployment of this size would raise it.
- The benchmarks of the feed and of the search call the handlers through `httptest`, without the network, as in 13.1.
- The test suite on PostgreSQL (`scripts/test-docker.sh`) takes longer than the one on SQLite; its time is not part of the targets.
