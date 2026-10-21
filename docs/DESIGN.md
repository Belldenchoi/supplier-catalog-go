# Design notes

## The invariant

For each `(supplier, SKU)`, the stored version never decreases. Products have one primary key, so duplicate delivery cannot insert a second identity. The withdrawal flag belongs to that versioned record; the key remains present after withdrawal.

Records carry complete product details rather than patches. An active record needs a name, integer `price_cents` and uppercase three-letter currency code; zero price is valid. A withdrawn record only needs identity, version and `withdrawn: true`. Optional tombstone details are discarded before duplicate comparison.

| Incoming record | Stored state | Action |
| --- | --- | --- |
| Valid, no stored identity | None | Add product, or create tombstone. |
| Lower version | Any | Count stale, leave stored state unchanged. |
| Equal version, same normalized details | Any | Count duplicate. |
| Equal version, different details | Any | Quarantine conflict; leave stored state unchanged. |
| Higher version, valid | Any | Replace, retain before/after audit. |
| Invalid identity/version/details | Any | Quarantine; keep valid existing state. Identifiable lower-version invalid details are stale. |

Conflicting equal versions cannot be resolved independent of arrival order: the first accepted value is retained, the conflict is explicit, and the supplier must send a higher version. For non-conflicting events, final product state is independent of arrival order. The test suite checks shuffled and repeated events.

## Persistence boundary

bbolt is a small, pure-Go embedded transactional store. Compared with an in-memory map or rewriting JSON files, it provides crash recovery, atomic commits, and a process lock without a database service. See the [upstream documentation](https://pkg.go.dev/go.etcd.io/bbolt) for transaction and sync behavior.

Buckets store schema metadata, products, runs, committed cursors, changes, and issues. One batch uses one read/write transaction to:

1. Reload the run and compare its expected cursor.
2. Verify supplier/update/cursor identity, required records, and no pagination cycle.
3. Apply version rules, append before/after changes, quarantine invalid records, and resolve superseded issues.
4. Mark that cursor committed and advance to the next cursor.
5. Mark the run finished only if this batch explicitly ends the feed.

HTTP happens outside the transaction. Another goroutine committing the same cursor makes the second commit return a progress-change signal, so the engine reloads progress instead of applying the batch again. The CLI holds the database's OS lock for its lifetime, deliberately allowing one operator process at a time.

## Failure points

| Failure point | Durable state after restart |
| --- | --- |
| Before the first HTTP call | Started run with cursor `0`; existing catalog intact. |
| While fetching/decoding | Previous batches retained; current cursor unchanged. |
| In the middle of the transaction | Uncommitted changes rolled back; current cursor unchanged. |
| After commit, before CLI prints | Batch and next cursor already durable; resume cannot double count it. |
| Supplier response lacks explicit terminal marker | Whole response rejected; never declared complete. |

`running` is advisory: an abrupt kill cannot persist an interruption marker. `next_cursor` and `finished_at` establish whether the feed was exhausted. `blocked` also retains an actionable transport/protocol error; row-level problems live in `issues` and are not confused with a failed fetch.

The current run's checkpoint and any previous run's issue resolutions commit together. An unresolved issue requires a strictly newer valid product version or explicit staff acknowledgment with a note. Acknowledgment only records a discard decision. Historical change/rejection counters describe what happened during ingestion and never decrease.

## Supplier protocol

```http
GET /v1/updates/delta/batches?cursor=1
Accept: application/json
```

```json
{
  "supplier_id": "northstar",
  "update_id": "delta",
  "cursor": "1",
  "next_cursor": "2",
  "records": [
    {"sku":"TEA","version":1,"name":"Mountain tea","price_cents":899,"currency":"USD","withdrawn":false},
    {"sku":"LAMP","version":4,"withdrawn":true}
  ]
}
```

All five envelope fields are required; `records` must be an array, including when empty. The last batch has `"next_cursor": null`. Cursors are opaque to the importer; only the fictional server uses zero-based numeric cursors. The source URL and update ID are pinned in the run. HTTP timeout is ten seconds, response size is at most 2 MiB, and a batch contains at most 2,000 records. Unknown JSON fields and concatenated documents are rejected to surface schema mistakes.

Custom fictional snapshots can be served with `catalog supplier --fixtures DIRECTORY`. Copy the shape of `internal/supplier/fixtures/baseline.json`; the file name must match `update_id`. Do not edit a snapshot already being imported: create a new update ID instead.

The adapter trusts the supplier's identity, version ordering and truthful terminal marker. It cannot infer that the supplier secretly omitted a batch or reset its version counter. A production protocol may need signed manifests, total counts or checksums according to that supplier's guarantees.
