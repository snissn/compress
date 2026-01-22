# Runbook: LevelDB+Snappy evaluation harness (A/B/C/D) and how to publish results

This runbook is for a future agent to **implement** a local evaluation tool and then **update** `improvements_note.md` with measured numbers.

The core question:

> Under LevelDB’s 32KB data-block model, do app-level encoding changes reduce on-disk `.sst` bytes enough that we can disable Snappy (and save CPU) without growing the DB?

---

## 0) Prereqs / environment sanity

1. Confirm you can build Go in this repo.

   In this environment we’ve seen a mismatch where `go` in `PATH` reports `go1.25.4` but `GOROOT` points at `go1.25.5`, which causes compile failures like:

   - `compile: version "go1.25.5" does not match go tool version "go1.25.4"`

   Fix by using a consistent Go toolchain (example on this machine):

   - Use `/Users/michaelseiler/.gvm/gos/go1.25.5/bin/go` explicitly, or
   - Adjust `PATH` so `which go` and `go env GOROOT` match.

2. Confirm you have the input file:

   - `../celestia-db.head.jsonl` (base64 JSONL export)

3. Pick an output directory (will be large; multiple DBs):

   - e.g. `./_leveldb_eval/`

---

## 1) What you need to build

Add a small Go command to this repo, for example:

- `cmd/leveldb-eval/` (new)

It should:

1. Read a (sorted) `(key,value)` stream from the JSONL file.
2. Optionally apply a **value transform** (“before” vs “after” encoding).
3. Build a LevelDB database with a chosen **block size** and **compression** option.
4. Force a full compaction.
5. Measure on-disk size and basic read/write performance.
6. Emit a machine-readable results file (JSON) and a human table (Markdown).

### 1.1 CLI shape (suggested)

Suggested flags:

- `--input ../celestia-db.head.jsonl`
- `--out ./_leveldb_eval`
- `--limit 0` (0 = all rows; otherwise first N)
- `--compression snappy|none`
- `--block-size 32768`
- `--restart-interval 16`
- `--transform before|after1|after2`
- `--run-bench` (optional)
- `--bench-gets 1000000`
- `--bench-scan` (optional)

---

## 2) Implementation details (recommended approach)

### 2.1 Use a real LevelDB implementation in-process

Use a Go LevelDB binding/library that supports configuring:

- block size
- restart interval
- compression: Snappy vs none

Common choices:

- `github.com/syndtr/goleveldb/leveldb` (widely used)

Configure options to match Celestia as closely as practical:

- `BlockSize`: `32 * 1024`
- `BlockRestartInterval`: `16`
- `Compression`: Snappy vs none
- Consider setting `WriteBuffer` large enough to reduce flush overhead (but keep it constant across variants).

### 2.2 Bulk-load in sorted key order

LevelDB is optimized for sequential inserts in key order. The JSONL file appears to be in key order; still, add a safety check:

- Verify keys are non-decreasing lexicographically; if not, fail fast (or implement an external sort mode).

Write strategy:

- Use `WriteBatch` and flush periodically (e.g., every ~5–50MB of accumulated data or every N records).
- Keep batch sizing constant across A/B/C/D.

After ingest:

- Call `CompactRange(nil,nil)` (or the full-range equivalent).
- Close the DB (ensures all files are flushed).

### 2.3 Measure on-disk size the same way each time

Report at least:

- `sst_bytes`: sum of `*.sst` file sizes
- `dir_bytes`: full directory size (walk all files)

Also record:

- number of `.sst` files
- optional: bytes by level (if exposed in properties)

### 2.4 Capture LevelDB stats/properties (if supported)

If the binding supports properties, store them for later inspection:

- `leveldb.stats`
- `leveldb.sstables`
- `leveldb.blockpool`

Write them to `./_leveldb_eval/<variant>/properties.txt`.

---

## 3) Defining “before” and “after” transforms (from the dump alone)

This harness can’t change Celestia consensus code. It can only rewrite the **bytes** in the dump to model candidate encodings.

Therefore, implement transforms as *approximate models* of “what the new state encoding could look like”.
Be explicit in the results about what was modeled.

### 3.1 `before` transform (identity)

```go
func TransformBefore(key, val []byte) []byte { return val }
```

### 3.2 `after1` transform (remove `Any.type_url` strings only)

Goal: model the impact of replacing long `Any.type_url` strings with a compact tag while leaving payload bytes intact.

Algorithm (for values that match the observed wrapper + outer Any pattern):

1. Parse wrapper: `<zigzag(height)> <payload_len> <payload>`
2. Parse outer `Any`:
   - `type_url` (ASCII)
   - `value` (inner bytes)
3. Replace `type_url` with a small numeric ID:

Example output encoding (simple, not protobuf):

```
After1 = <wrapper height> <uvarint inner_len> <uvarint type_id> <inner bytes>
```

Where `type_id` is assigned by a table, e.g.:

- 1 = BaseAccount
- 2 = DelayedVestingAccount
- 3 = ContinuousVestingAccount
- 4 = ModuleAccount
- 255 = unknown/other

Fallback behavior:

- If parsing fails, return original bytes (don’t crash the run).

### 3.3 `after2` transform (model “compact account record”)

Goal: approximate the bigger app-level idea:

- remove outer `Any.type_url`
- store bech32 address as raw bytes
- store pubkey as raw bytes (no nested Any)

Only do this for the dominant type (`BaseAccount`) first; keep other types as `after1` (or identity).

For BaseAccount, parse enough protobuf fields to extract:

- `address` string (bech32)
- `pub_key` nested Any (extract 33-byte compressed secp256k1 pubkey)
- `account_number` (uvarint)
- `sequence` (uvarint, optional)

Then re-encode in a compact binary format like:

```
AccountV1 {
  kind: u8 = 1
  flags: u8 (bit0=has_pubkey, bit1=has_sequence)
  addr20: [20]byte
  pubkey33?: [33]byte
  account_number: uvarint
  sequence?: uvarint
}

After2 = <wrapper height> <uvarint len(AccountV1)> <AccountV1 bytes>
```

Notes:

- Bech32 decode: use a library (preferred) or implement minimal decoding.
- If BaseAccount parse fails, fall back to `after1` or identity.

### 3.4 CommitInfo modeling (optional)

CommitInfo entries are few (257), but very large (~1269B each).

Two options:

- Keep them unchanged (simpler; small impact on overall DB size).
- Model “manifest + per-height delta” as described in `improvements_note.md`:
  - store manifest once under a special key
  - store per-height delta values under `s/<height>` keys

If you model it, be explicit in the results section that the harness is rewriting semantics.

---

## 4) Running the experiments (A/B/C/D)

Run these 4 variants with identical settings except for transform and compression:

| Variant | transform | compression |
|---|---|---|
| A | before | snappy |
| B | before | none |
| C | after2 (or after1) | snappy |
| D | after2 (or after1) | none |

For each run:

- Use a fresh output dir (delete old DB).
- Record:
  - ingest time
  - compaction time
  - `sst_bytes`, `dir_bytes`
  - `.sst` count
  - properties (if available)

### 4.1 Read benchmarks (optional but recommended)

At minimum:

- Random `Get` throughput on a deterministic key set (e.g., every Nth key from the dump, or PRNG sample with fixed seed).
- Full scan throughput (iterate).

Make sure the benchmark is the same across variants:

- same keys
- same number of ops
- same process layout (open DB once, then run loop)

If you can’t reliably simulate “cold cache”, focus on warm-cache numbers; Snappy decode CPU still shows up clearly.

---

## 5) Publishing results into `improvements_note.md`

Add a new section near “How to evaluate these wins under LevelDB + Snappy (32KB blocks)”:

- **Measured results** (include date/time and machine details)
- A table like:

| Variant | transform | compression | sst_bytes | dir_bytes | ingest_s | compact_s | gets_ops/s | scan_MB/s |
|---|---|---|---:|---:|---:|---:|---:|---:|

Also include a short interpretation paragraph:

- Does **D ≈ A** on `sst_bytes`?
- If yes, what is the CPU win (ingest/compaction/get)?
- If no, where does it fail (Snappy ratio changes, more blocks, etc)?

Finally, document exactly what `after1/after2` means in the harness (so readers don’t mistake it for a real Celestia migration).

---

## 6) Nice-to-have: a fast “block simulator” subcommand

If you want quick iteration without building whole DBs, add:

- `cmd/leveldb-eval --mode=blocksim`

It should:

- implement LevelDB data block packing (prefix-compressed keys + restart array)
- cap at 32KB blocks
- apply Snappy and the “only keep if it wins” heuristic

Output:

- estimated on-disk data-block bytes for each variant
- estimated “compressed vs uncompressed block mix”

Use it to predict whether “after + no compression” is plausible before running full DB builds.

