# Notes: Compression Opportunities Seen in `celestia-db.head.jsonl`

This doc is written for two audiences:

1. **Celestia-node / Cosmos SDK devs** who generally understand the workload (accounts, stores, commits), but aren’t familiar with the *exact* on-disk byte layout.
2. **KV-store / “value slab” devs** building a **data-agnostic** key/value store that should automatically benefit *future* workloads without app changes.

The goal here isn’t “make this one dataset small” (Celestia *can* do that in app land). The goal is to separate:

- (A) what Celestia could do if they *wanted* to optimize their on-disk representation, and
- (B) what a generic KV store can do automatically for any opaque `[]byte` values.

---

## What this export contains (concrete)

Each JSONL row is:

```jsonc
{"key":"<base64>", "val":"<base64>", "encoding":"base64"}
```

After base64-decoding:

- **Keys** are bytes.
- **Values** are bytes.

In this file:

- Total rows: **1,048,576**
- Two key families:
  - `s/<height>`: **257 rows** (heights `9499500..9499756`)
  - `s/k:acc/...`: **1,048,319 rows** (all under the `acc` store)

Value-size clusters (decoded bytes):

- **169 bytes**: 949,027 rows (~90.5%)
- **94 bytes**: 72,606 rows (~6.9%)
- Then a long tail.

### “Account” value framing (important detail)

For the dominant `s/k:acc/...` values, the bytes are *not* “just protobuf”. They look like:

```
<varint zigzag-int> <varint payload_len> <payload_bytes...>
```

Example (169-byte value), shown as hex with annotations:

```
d8 cd 87 09   a3 01   0a 20 2f 63 6f 73 6d 6f 73 2e ...
^^^^^^^^^^^   ^^^^^   ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^
v_prefix      len     payload begins with protobuf Any.type_url
```

- `d8 cd 87 09` decodes as varint **18,999,000**, which is **zigzag(9,499,500)**.
- `a3 01` decodes as varint **163**, the length of the remaining payload.
- The payload starts with `0a 20 ...` which matches protobuf `Any` field `type_url` (tag `0x0a`) length 32 (`0x20`).

So the real “meaningful” payload is a protobuf `Any`, but it’s wrapped with a small, repeated prefix.

### What’s inside the `Any` payload (high level)

The payload overwhelmingly contains:

- `Any{ type_url="/cosmos.auth.v1beta1.BaseAccount", value=<BaseAccount bytes> }`

Within those `BaseAccount` messages we frequently see:

- bech32 address strings like `celestia1...` (ASCII)
- another nested `Any` for pubkey type URL, usually `/cosmos.crypto.secp256k1.PubKey`

This is “highly self-similar” bytewise data: large constant substrings + a few small fields that differ.

---

## Summary: measured mix + estimated size wins

This section is an attempt to answer “how much does this matter?” with concrete numbers.

### Measured record mix (from this file)

All sizes below are **decoded `val` byte lengths** (i.e., after base64 decode).

| Record kind (how identified) | Count | Avg bytes/record (current) | Common sizes |
|---|---:|---:|---|
| `CommitInfo` (`key` starts with `s/<height>`) | 257 | 1268.7 | 1267–1269 |
| `BaseAccount` (`Any.type_url=/cosmos.auth.v1beta1.BaseAccount`) | 1,047,799 | 163.8 | 169 (90.6%), 94 (6.9%), then small tails |
| `DelayedVestingAccount` | 275 | 224.6 | ~213–217 (common), with a long tail |
| `ContinuousVestingAccount` | 225 | 238.1 | ~224–226 (common), with a long tail |
| `ModuleAccount` | 5 | 124.6 | 111–140 |

Overall (all 1,048,576 rows): **~164.1 bytes/value on average** in this export.

### Estimated “after” if Celestia changes the encoding (A)

These are **application-level, consensus-critical** changes (state encoding migration territory).

Key measured constants that drive the estimate:

- In the dominant BaseAccount blob:
  - address string length is **47 bytes** (`"celestia1..."`)
  - pubkey is stored as `Any{type_url="/cosmos.crypto.secp256k1.PubKey", value=<PubKey>}` where:
    - pubkey `type_url` length is **31 bytes**
    - pubkey message encodes a 33-byte compressed key (`secp256k1.PubKey{ key: bytes33 }`)
  - outer account `Any.type_url` length is **32 bytes**

Assumptions for the “after” estimate:

- Replace outer `Any.type_url` with a small kind tag (e.g., `AccountKind` varint).
- Store address as raw bytes (assume **20 bytes**) instead of bech32 string.
- Store pubkey as raw bytes (33 bytes) instead of nested `Any`.
- Keep the outer “version + length + value” wrapper (since it appears to be part of the underlying node/store encoding).

**BaseAccount (dominant case) – common shapes**

| Shape (what the bytes imply today) | Before | After (estimate) | Notes |
|---|---:|---:|---|
| Has pubkey, has sequence (most common) | 169B | ~72B | drops `Any` type URLs + bech32 address |
| No pubkey, no sequence | 94B | ~35B | same idea, but no pubkey savings |

**BaseAccount (overall average across its variants):** ~163.8B → **~69.4B** (≈ **57.6%** smaller).

**Other account types (rough estimates)**

These estimates assume the same “remove outer type_url + remove bech32 + remove pubkey Any” applies because these messages embed a BaseAccount-like structure.

| Account kind | Before avg | After avg (estimate) | Notes on what was “removed” |
|---|---:|---:|---|
| DelayedVestingAccount | 224.6B | ~117B | save ~45B (outer type_url) + 27B (bech32→bytes) + ~37B (pubkey Any, present in ~96% of these) |
| ContinuousVestingAccount | 238.1B | ~128B | save ~48B + 27B + ~37B (pubkey Any, present in ~94% of these) |
| ModuleAccount | 124.6B | ~64B | save ~34B + 27B (no pubkey Any observed in this dataset) |

**CommitInfo (app-aware delta format)**

If you store a one-time store-name manifest and then per-height `changed_bitset + hashes_for_changed_stores`,
the observed change rate implies roughly:

- Before: ~1268.7B/value
- After: ~`3B(bitset) + 32B*6.14(avg changed stores)` ≈ **~199B/value** (≈ **84%** smaller)

If you apply these app-level ideas across this export, the overall average would move roughly:

- **~164.1B/value → ~69.5B/value** (≈ **57.7%** smaller)

### Estimated “after” if the KV store auto-detects patterns (B)

These do **not** require any Celestia changes; values remain opaque `[]byte`.

The most promising generic, pointwise-friendly codec for this dataset is **template + byte-mask patch** enabled per partition/length-bucket.

Measured patch payload sizes (relative to a fixed template within a bucket):

> Note: the template itself is stored once per page; its cost amortizes to ~0 bytes/record for reasonable page sizes.

| Bucket | Before | Patch payload avg | Savings |
|---|---:|---:|---:|
| Fixed-length 169B values (dominant) | 169B | ~94.8B | ~44% |
| Fixed-length 94B values | 94B | ~51.7B | ~45% |

Weighted over the BaseAccount population, this comes out to roughly:

- BaseAccount: ~163.8B → **~91.8B** (≈ **44%** smaller)

For `CommitInfo` values, a byte-mask patch works *best* as “delta-to-previous with periodic checkpoints”:

- CommitInfo: ~1268.7B → **~385B** average patch payload (≈ **70%** smaller)

These DB-level gains are smaller than the app-level “remove Any + remove bech32” wins (because the DB can’t change semantics),
but they apply automatically to any future workload that exhibits “fixed length + few changing bytes”.

Roughly applied to this export (i.e., “the big fixed-length buckets trigger template+patch”), the overall average would move:

- **~164.1B/value → ~92.0B/value** (≈ **44%** smaller)

---

## How to evaluate these wins under LevelDB + Snappy (32KB blocks)

Celestia uses LevelDB (via the Cosmos SDK store stack) with **Snappy compression enabled**.
LevelDB compresses at the **data-block** level (often configured around **32KB** uncompressed blocks), not “per value”.

That changes the intuition of “before bytes vs after bytes”:

- If you remove repeated strings (like long protobuf `Any.type_url`), **raw values get smaller**.
- But you might also remove a lot of redundancy that Snappy was exploiting, so the **Snappy ratio may worsen**.
- The only way to know “what wins on disk and CPU” is to measure in a setup that matches LevelDB’s block format + compression heuristics.

### The A/B matrix you want (4 variants)

Use the same logical KV stream (same key order and same keys) and build four databases:

| Variant | App encoding | LevelDB compression | Why it matters |
|---|---|---|---|
| A | before | Snappy | baseline “today” |
| B | before | none | isolates *Snappy-only* benefit |
| C | after | Snappy | “best possible disk” if Snappy still helps |
| D | after | none | tests if app fixes can replace Snappy |

Your stated “big win” condition is essentially:

> If **D (after + no compression)** is within ~a few % of **A (before + Snappy)** on `.sst` bytes, then you can likely disable Snappy and still keep disk size comparable — while saving CPU on reads/writes/compactions.

### What to measure (minimum set)

At minimum, for each variant:

- **Space**
  - Total `.sst` size (sum of SSTable files).
  - Total DB dir size (includes logs, manifests, etc).
- **Write/compaction CPU proxies**
  - Total ingest time (bulk load same dataset).
  - Time for `CompactRange(nil,nil)` (or “compact entire DB”).
- **Read perf**
  - Random `Get` latency/throughput on a fixed key set (cold cache + warm cache).
  - Full iteration throughput (scan).

If your LevelDB binding exposes internal stats/properties, also capture:

- `leveldb.stats` (compaction summary)
- `leveldb.sstables` (table sizes per level)
- compression effectiveness (if available)

### Measured results (2026-01-22, Apple M3, macOS 15.6)

Run details:

- Tool: `cmd/leveldb-eval` (goleveldb v1.0.0, Go 1.25.5).
- Input: `../celestia-db.head.jsonl` (1,048,576 records).
- Options: block_size=32KB, restart_interval=16, write_buffer=64MB, batch_bytes=32MB.
- Full `CompactRange` after ingest.
- Bench: 1,000,000 random gets + full scan (warm cache).
- `sst_bytes` counts table files (`.sst` + `.ldb`).

| Variant | transform | compression | sst_bytes | dir_bytes | ingest_s | compact_s | gets_ops/s | scan_MB/s |
|---|---|---|---:|---:|---:|---:|---:|---:|
| A | before | snappy | 108,759,804 | 108,774,147 | 2.056 | 0.790 | 881,061 | 1,244.2 |
| B | before | none | 204,831,924 | 204,855,129 | 2.189 | 1.322 | 1,020,375 | 1,892.1 |
| C | after2 | snappy | 71,075,184 | 71,083,848 | 2.986 | 0.662 | 1,176,289 | 904.1 |
| D | after2 | none | 96,603,455 | 96,614,436 | 2.882 | 0.446 | 1,218,311 | 1,239.8 |

Interpretation:

- **D (after2 + none)** is ~11% smaller than **A (before + snappy)** on `sst_bytes` (96.6MB vs 108.8MB). This satisfies the “D ~ A” condition, so disabling Snappy looks viable if the modeled encoding is real.
- Snappy still helps even after the app-level changes: **C** is ~26% smaller than **D** (71.1MB vs 96.6MB).
- Read CPU proxy improves without Snappy: **D** has the highest get throughput (~1.22M ops/s). Full scan time is also fastest in D (~0.074s vs 0.156s in A).
- Raw value bytes dropped from 172,079,536 -> 64,993,306 (~62% smaller) under the **after2** model.

Modeling note: `after2` here is a **synthetic encoding** (remove `Any.type_url`, bech32→raw bytes, pubkey Any→raw 33B) applied only to BaseAccount; other account types use the `after1` “type_id + inner bytes” fallback. These numbers are therefore directional, not a guaranteed migration outcome.

### Important LevelDB detail: blocks are only kept compressed if they “win”

LevelDB doesn’t always store Snappy output — it typically compresses a block and only keeps the compressed form if it is
**meaningfully smaller** than the uncompressed form (to avoid overhead).

So even “before + Snappy” may contain a mix of:

- compressed blocks (Snappy wins),
- uncompressed blocks (Snappy doesn’t help enough).

That’s one reason why “disable Snappy” is not always a huge regression in practice — and why D≈A is plausible if you shrink raw bytes enough.

### Two practical ways to run the evaluation

#### Option 1 (recommended): build real LevelDB DBs and compare

This gives the most trustworthy result because it includes:

- key prefix compression inside blocks,
- block restart interval behavior,
- block compression heuristics,
- index/filter/meta blocks,
- and compaction effects.

High-level steps:

1. Stream the `(key,value)` pairs in sorted key order.
2. Open LevelDB with Celestia-like options (notably block size ≈ 32KB).
3. Bulk-load all records (ideally in order).
4. Force a full compaction.
5. Close DB.
6. Measure `.sst` bytes + run read benchmarks.

#### Option 2 (fast proxy): simulate LevelDB data blocks + Snappy

This is faster to implement and can be good enough to answer:

- “will Snappy still help after the app change?”
- “are we close to D≈A?”

The simulator should:

- Build LevelDB **data blocks** (prefix-compressed keys + restart array, typical restart interval 16).
- Cap blocks at ~32KB uncompressed.
- Snappy-encode each block and apply the “store compressed only if it wins” heuristic.
- Sum resulting block sizes (+ per-block trailer).

This ignores index/filter blocks and compactions, but for value-heavy workloads it can still be directionally correct.

### Decision criteria (how to interpret results)

After you have A/B/C/D:

- If **D ≲ A** (similar `.sst` bytes) and **D is faster** on reads/writes: app-level encoding has effectively “replaced Snappy”.
- If **C ≪ A** and read CPU is OK: keep Snappy; app-level changes + Snappy is “best disk”.
- If **C ≈ A** but CPU is worse (more blocks, worse cache locality, etc): app changes may have removed redundancy Snappy used; re-check the chosen app format.

Also watch for second-order effects:

- Smaller values usually reduce compaction work and write amplification.
- More blocks (even if total bytes are similar) can hurt cache behavior and random read latency.

If you want to implement this evaluation harness locally, see `leveldb_snappy_eval_runbook.md`.

## A) App-level improvements (Celestia-node / Cosmos SDK)

These require **changing the application’s state encoding**, which is **consensus-critical** (i.e., it changes the bytes that contribute to app hash). That typically means:

- a state migration,
- a version bump, and
- careful rollout.

That said, the dataset makes a few “obvious wins” very clear.

### A1) Stop storing `google.protobuf.Any` headers everywhere (replace with a small type tag)

**What’s happening now (conceptual “before”)**

Many stored records look like:

```text
value = <prefix> + Any{
  type_url: "/cosmos.auth.v1beta1.BaseAccount",          // repeated ASCII
  value: BaseAccount{
    address: "celestia1....",                             // repeated bech32 prefix
    pub_key: Any{
      type_url: "/cosmos.crypto.secp256k1.PubKey",        // repeated ASCII
      value: <pubkey bytes>,
    },
    account_number: ...,
    sequence: ...,
  }
}
```

**Suggestion (“after”)**

Replace the outer `Any` with a compact discriminator, so you don’t store the long `type_url` strings N times.

One workable pattern:

```proto
enum AccountKind {
  ACCOUNT_KIND_UNSPECIFIED = 0;
  ACCOUNT_KIND_BASE        = 1;
  ACCOUNT_KIND_DELAYED     = 2;
  ACCOUNT_KIND_CONTINUOUS  = 3;
  ACCOUNT_KIND_MODULE      = 4;
}

message AccountRecord {
  AccountKind kind = 1;
  bytes       body = 2; // concrete message bytes (BaseAccount / Vesting / ...)
}
```

Pseudo-code:

```go
func marshalAccount(a AccountI) []byte {
  kind := kindOf(a)            // 1 byte-ish varint in practice
  body := marshalConcrete(a)   // no Any wrapper
  return protoMarshal(AccountRecord{Kind: kind, Body: body})
}
```

**Why it helps**

- Removes repeated `type_url` ASCII strings (huge win for both size and CPU).
- Makes “pointwise” reads cheaper because there’s less stuff to parse.

### A2) Avoid storing bech32 strings in state (store raw address bytes)

Bech32 is great for UX; it’s not great as the canonical stored representation.

**Before**

```text
BaseAccount{ address: "celestia1qqqqr2u6rfy..." }
```

**After**

```text
BaseAccount{ address_bytes: <20 bytes> }  // or whatever your address length is
```

Pseudo:

```go
addrBytes := address.Bytes()
// only encode bech32 at boundaries (CLI / JSON / APIs)
```

### A3) Split “rare / optional / large” fields into side-stores (normalize)

If most accounts are “base” and only a minority have vesting/module fields:

**Before**

- one store where values must represent all variants (forces `Any`, oneof, or lots of optional fields)

**After**

- `acc/base/<addr>` → base account minimal record
- `acc/vesting/<addr>` → vesting-only payload (present only when needed)
- `acc/module/<addr>` → module-only payload

This reduces:

- average value size,
- variance in value shape, and
- the amount of “dead bytes” written in the dominant case.

### A4) Commit-info: store a manifest once + per-height deltas

The `s/<height>` entries contain commit info for **24 stores**, including store name strings and 32-byte hashes.

From the dataset:

- store names are stable across heights,
- only **~6–9 stores** change per height,
- 6 stores change *every* height (`acc`, `bank`, `distribution`, `mint`, `slashing`, `staking`),
- many others change rarely.

**Before (conceptual)**

```proto
message CommitInfo {
  int64 version = 1;
  repeated StoreInfo store_infos = 2;
}
message StoreInfo {
  string name = 1;      // repeated across every height
  CommitID commit_id = 2;
}
message CommitID {
  int64 version = 1;
  bytes hash = 2;       // 32 bytes
}
```

**After**

Store once:

```text
CommitManifest{ stores: ["acc","authz","bank",...,"warp"] }
```

Then per height:

```text
CommitDelta{
  height: H,
  changed_bitset: 24 bits,
  new_hashes: [bytes32 ...] // only for stores whose bits are set
}
```

Pseudo:

```go
func writeCommitDelta(prev, cur [24][32]byte) CommitDelta {
  var changed Bitset24
  var hashes [][32]byte
  for i := range 24 {
    if prev[i] != cur[i] {
      changed.Set(i)
      hashes = append(hashes, cur[i])
    }
  }
  return CommitDelta{Changed: changed, Hashes: hashes}
}
```

For random access, checkpoint every N heights with a full snapshot.

---

## B) KV-store improvements (automatic + data/usage agnostic)

Here’s the key constraint: the DB must treat values as **opaque bytes**. No protobuf knowledge, no “cosmos account” assumptions. The system should still notice patterns like “these bytes are highly repetitive” and exploit them safely.

### B0) Baseline (“before”) storage model (generic)

This is the baseline most KV stores start with:

```text
slab.append(valueBytes) -> stores exact bytes
```

Optional compression variants:

- compress big blocks for throughput/ratio (hurts pointwise reads)
- compress each value independently (pointwise OK, but overhead and maybe worse ratio)

### B1) Automatically partition the keyspace (no semantics needed)

Most real workloads are “mixtures” of sub-workloads (different tables/stores/prefixes).

**Approach**

- Partition by a cheap key prefix, e.g. first 4–16 bytes of the key.
- Keep independent stats per partition:
  - value length histogram
  - rolling hashes / fingerprints
  - similarity estimates
  - compressibility estimates (periodic background trials)

Pseudo:

```go
type PartitionID uint64

func partitionOfKey(key []byte) PartitionID {
  // e.g. take first 8 bytes (or hash them) with a small prefix length cap.
  return siphash(key[:min(len(key), 16)])
}
```

In this dataset, partitioning by key prefix would immediately isolate `s/k:acc/...` as “one huge hot partition”.

### B2) Adaptive codec selection per partition (choose what works, automatically)

For each partition, periodically decide:

- store raw
- store per-value compressed with a rotating dictionary (pointwise)
- store “template + patch” deltas (pointwise)
- store deduplicated values (pointwise, if repeats are exact)

Important property: **once a page is written, its codec is fixed and self-describing** so reads don’t depend on “current settings”.

Pseudo sketch:

```go
type Codec uint8
const (
  CodecRaw Codec = iota
  CodecZstdDictPerValue
  CodecTemplatePatch
  CodecDedup
)

func chooseCodec(stats PartitionStats) Codec {
  if stats.ExactRepeatRate > 0.10 {
    return CodecDedup
  }
  if stats.FixedLengthRate > 0.80 && stats.TemplatePatchSavings > 0.20 {
    return CodecTemplatePatch
  }
  if stats.DictSavings > 0.15 {
    return CodecZstdDictPerValue
  }
  return CodecRaw
}
```

### B3) A pointwise-friendly “template + patch” codec (opaque bytes)

This is the generic version of “template + byte-mask delta”, but implemented *without* any schema knowledge.

#### When to enable (automatic detection)

Enable when a partition shows:

- most values have the same length (or can be bucketed by length),
- bytes are highly similar (few positions change),
- and the patch representation wins by a configured threshold.

#### One practical on-disk page format (example)

For fixed-length values within a page:

```text
PageHeader {
  codec = TEMPLATE_PATCH
  value_len = L
  n = number of records
  template = L bytes
  offsets[n+1] = uint32 (byte offsets into payload region)
}

payload region: record_0 || record_1 || ... || record_{n-1}

record_i:
  mask = ceil(L/8) bytes
  changed_bytes = bytes, in increasing byte index order
```

Pointwise decode of `record_i`:

```go
func decodeTemplatePatch(template []byte, mask []byte, changed []byte) []byte {
  out := make([]byte, len(template))
  copy(out, template)
  j := 0
  for pos := 0; pos < len(out); pos++ {
    if (mask[pos>>3]>>(pos&7))&1 == 1 {
      out[pos] = changed[j]
      j++
    }
  }
  return out
}
```

Notes:

- This codec is **fast** (copy + patch).
- It supports **true pointwise** reads: you read the page header once and only the bytes for the record you want.
- It’s generic: it doesn’t care what the bytes “mean”.

This dataset is a textbook case where the DB could auto-detect it, because two fixed lengths dominate and similarity is high.

### B4) Per-value dictionary compression (generic + pointwise)

If you already have “zstd + dict” working, the generic way to keep pointwise reads is:

- compress each value independently,
- but use a per-partition dictionary (or per-length-bucket dictionary),
- rotate dict versions over time (store dict ID in the page/record header).

Example record layout:

```text
record_i:
  dict_id: u32
  compressed_len: varint
  compressed_bytes...
```

Pseudo:

```go
func encodeZstdDict(dictID uint32, dict []byte, value []byte) []byte {
  c := zstdCompressWithDict(dict, value)
  return append(u32le(dictID), varint(len(c))..., c...)
}
```

This is robust across many workloads, but CPU cost is higher than template+patch.

### B5) Commit-info as a “generic DB win” (without understanding commits)

Even if the DB doesn’t understand “stores” or “hashes”, the `s/<height>` values exhibit:

- same structure,
- many repeated substrings (store names),
- small per-record changes.

That means both:

- per-partition dict compression, and/or
- template+patch

can trigger automatically and help here without app changes.

### B6) Safety / operational notes for automatic schemes

For a DB that self-tunes codecs, a few rules keep it safe:

- **Never rewrite existing pages in place** (append-only pages + manifest).
- **Self-describing pages**: codec + parameters + dict/template IDs must be in the page header.
- **Graceful fallback**: if stats are unclear, write `CodecRaw` (correctness > ratio).
- **Background training**: do expensive analysis/dict building off the write path; apply only to new pages.
- **Hard caps**: e.g., don’t enable template+patch unless savings exceed X% and decode CPU stays bounded.

---

## Quick takeaway

- **Celestia can get huge wins** by removing repeated `Any.type_url` strings, normalizing account variants, and delta-encoding commit info. That’s app work + migration.
- **A generic KV store can still benefit** by:
  - auto-partitioning by key prefix,
  - auto-selecting codecs per partition/length bucket,
  - and offering a *pointwise-friendly* template+patch codec alongside per-value dict compression.
