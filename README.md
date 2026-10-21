# Supplier catalog updates

A Go command-line application for a shop importing a fictional supplier's catalog. Staff can start or resume an update, inspect exact changes, and resolve outstanding problems. The catalog and progress survive application restarts.

**Demo:** [3 minute 20 second walkthrough](docs/demo.mp4) · [written walkthrough](docs/DEMO.md) · [design and recovery guarantees](docs/DESIGN.md)

## Run

Requires **Go 1.23+**. No external database, supplier account, Docker, or C compiler is needed. The fictional Northstar supplier and its fixtures are embedded in the binary.

```sh
go mod download
go build -o bin/catalog ./cmd/catalog
```

On Windows, build with `go build -o bin/catalog.exe ./cmd/catalog` and use `./bin/catalog.exe` below. Run commands from the repository root. Put flags **after** the command.

In terminal 1, start the fictional supplier with a one-time outage on the second delta batch:

```sh
./bin/catalog supplier --fail-update delta --fail-cursor 1 --fail-count 1
```

In terminal 2:

```sh
# Seed three products. Save the JSON run ID if you want to inspect this run.
./bin/catalog start --update baseline

# Commits batch 0, then gets HTTP 503 on batch 1. Exits 1, status "blocked".
./bin/catalog start --update delta

# The catalog is still present; include withdrawn products to inspect tombstones.
./bin/catalog products --all
./bin/catalog status

# Replace RUN_ID with the delta run's id. This starts at batch 1, not batch 0.
./bin/catalog resume --run RUN_ID

# All batches are now consumed, but two data issues remain. Exit code is 2.
./bin/catalog changes --run RUN_ID
./bin/catalog issues --run RUN_ID

# A higher-version correction fixes both issues without editing the DB.
./bin/catalog start --update corrections
./bin/catalog issues
./bin/catalog issues --run RUN_ID --all
./bin/catalog status --run RUN_ID

# Replay the entire delta. No duplicate products, no regression or resurrection.
./bin/catalog start --update delta
./bin/catalog products --all
```

The final catalog has **five identities**, including one withdrawn lamp, so **four active products**. The mug stays at version 4, the notebook at 2, and the withdrawn lamp at 4. The replay has no catalog changes and does not reopen superseded invalid data.

Stop the supplier with Ctrl+C. Its one-time fault counter resets when it restarts; restart without fault flags for a healthy supplier. Each database command opens the same `data/catalog.db`. Use `--db another/path.db` to isolate a run.

### Demonstrate restart explicitly

```sh
./bin/catalog start --update baseline --db data/restart.db --max-pages 1
# Exits 3 after one committed batch. The process has now stopped.
./bin/catalog status --db data/restart.db
./bin/catalog resume --db data/restart.db --run RUN_ID
```

An abrupt process kill during a fetch can leave the persisted status as `running`. It does **not** mean completed: `next_cursor` is still present. Resume the same run. The tests also kill real child processes during fetch and during an uncommitted database transaction.

### Automated demo

Python 3 is optional and only used for demo tooling:

```sh
python scripts/demo.py --binary ./bin/catalog
# Windows: python scripts/demo.py --binary ./bin/catalog.exe
```

This launches its own supplier on an unused local port, uses a fresh temporary database, asserts the expected behavior, prints a readable transcript, and saves `docs/demo-transcript.json`. Every operation runs as a separate CLI process. It leaves your default catalog alone. To render the captioned video again, see [docs/DEMO.md](docs/DEMO.md).

## Commands and outcomes

All database commands output JSON. `catalog help` lists flags. `status` lists all runs or one selected run; `changes` retains before/after values; `issues` defaults to unresolved issues and `--all` includes their resolution history. `products` hides withdrawn items unless `--all` is supplied. Money uses integer minor units rather than floating point.

| Run status | Meaning | Next action |
| --- | --- | --- |
| `running` | Started; more batches remain. Can also indicate an abrupt interruption. | Resume after a stopped process. |
| `blocked` | HTTP, protocol or persistence error; failed batch not checkpointed. | Inspect `last_error`, fix the cause, resume the same run. |
| `interrupted` | Ctrl+C or `--max-pages`; committed work retained. | Resume the same run. |
| `finished_with_issues` | Explicit terminal batch received, but rejected records remain unresolved. | Import higher-version corrections or acknowledge unusable records. |
| `completed` | Terminal batch received and no unresolved issues remain. | Review counts and changes. |

Exit codes: **0** success, **1** error/blocked, **2** finished with unresolved issues, **3** interrupted. A read-only `status` command exits 0 when it successfully reads a run, regardless of that run's state. `go run` wraps application exit codes, so use a built binary for scripting.

A staff member can explicitly discard a rejected record after investigation:

```sh
./bin/catalog acknowledge --issue ISSUE_ID --note "Supplier confirmed this record should be discarded"
```

This records a resolution note; it does not apply the rejected payload. Valid records with a strictly higher version automatically resolve older issues for the same supplier/SKU. Same-version conflicts require a higher version, since there is no safe tie breaker. Historical rejected counts and issue records remain visible after resolution.

## Tests

```sh
go test ./... -count=1
go vet ./...
go test ./... -coverprofile=coverage.out
go tool cover -func=coverage.out
# With a C toolchain, e.g. Ubuntu CI:
go test -race ./...
```

Tests cover new/changed/withdrawn products, repeated and shuffled delivery, equal-version conflicts, unknown-SKU tombstones, supplier isolation, invalid records, corrected issues, pagination errors, missing terminal markers, HTTP outages/timeouts, transaction rollback, concurrent cursor commits, graceful interruption, and real process crashes with database reopen. CI runs on Linux and Windows, with a Linux race check.

## Assumptions and tradeoffs

- Identity is `(supplier_id, sku)`, stable across updates. SKU and update IDs have a documented restricted character set; names are trimmed. Supplier versions are positive signed 64-bit integers, strictly ordered **per product**, including withdrawals. Arrival order and local time are never used to select the winner.
- Updates are **immutable paginated delta feeds**. Their ID and cursors remain available across restarts; the first cursor is `"0"`. Omission never means withdrawal. A response must explicitly contain `next_cursor: null` to finish. If a real supplier cannot replay stable snapshots, its adapter needs a durable ingestion log before this contract can be met.
- Equal version and equal normalized content is a duplicate; equal version with conflicting content becomes an issue. Older identifiable records are ignored, including superseded invalid details. A strictly newer active record may intentionally reinstate a withdrawn product; older or equal active records cannot. Tombstones are retained indefinitely, even for unknown SKUs.
- Valid records in a batch are committed even when another record is invalid; invalid records are quarantined with their original payload and position. Transport/protocol failures reject the **whole batch** and preserve its cursor. Catalog changes become visible batch by batch, rather than swapping the entire catalog at the end.
- A single local operator process owns the database file. Another process fails clearly after a two-second lock timeout; it cannot start a second writer or inspect the database concurrently. This is a CLI intended for a small shop, with explicit manual retry after supplier recovery.
- Durability relies on bbolt transactions, fsync, and a local filesystem honoring flushes. A working disk is assumed. Keep the DB on local storage. Back up the closed DB; no database file is committed to Git.

## Unfinished production work

The requested workflow is implemented against fictional data. Production additions would include a real supplier adapter/authentication, automatic retry with backoff, scheduling, staff authentication/attributed audit users, online backup/restore and schema migration tooling, retention and indexing for large audit histories, and multi-process/web access. List commands currently load results into memory, and issue resolution scans issue history. There is no public app deployment; the submitted video is the demo deliverable.

## AI use

This submission was built with OpenAI Codex for design, Go implementation, test development, documentation, and generation of the captioned demo from actual CLI output. AI was also used to review failure cases and correct test/implementation issues found while running the project. The repository includes the implementation and verification steps so each design choice can be inspected and explained.

## Layout

```text
cmd/catalog/          CLI and supplier server entry point
internal/catalog/     version rules, durable store, import engine, recovery tests
internal/supplier/    HTTP adapter and embedded fictional supplier snapshots
scripts/              reproducible demo and video renderer
docs/                 design notes, demo video, transcript and interview guide
```
