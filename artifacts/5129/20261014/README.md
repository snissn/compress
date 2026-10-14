# #5129 bounded original-engine construction packet

Base compression source: `87fb149e472104f0c2fa7a961b2493c4372603dc`.
Gomap producer/runtime source: `075ae77487778a872384a935c6d550359ca76452`;
producer native worktree artifact head: `a9553da930ae6568a56c7e1bcf51a400cafcf86d`.
This is the independently reviewable compression prerequisite for
[gomap #5129](https://github.com/snissn/gomap/issues/5129), not native qualification.

## Source and API

The new continuation stays inside original `zstd.Decoder`. Original Header,
FSE normalization/spread, Huffman construction and scalar sequence interpretation
are shared with synchronous consumers; existing assembly fast paths remain.
`NewBoundedReader`, `Begin`/`BeginStream`, `InputRequest`/`FeedInput`, `Step`,
`RawResult`, `ResetStep`/`ResetStreamStep`, and `CloseStep` use one caller-owned
reservation/admission interface. There is no internal scheduler, file owner,
dictionary registry, alternate index or ordinary whole-decode fallback.

Actual complete admitted Raw supports leaf groups and other requested complete
outputs. Spans retain necessary original history in individually admitted 32KiB
segments, with charged linked backward probes and scalar overlap. Single-segment
frames can require history equal to the entire value. Retention is truthful,
including input/wire/literal/dictionary/table/output overlap; it is not a promise
that arbitrary pointed values fit gomap's eventual integrated workspace cap.
The library handles original maximum128KiB compressed blocks through one
128KiB+18B caller-fed wire backing; frames/input can exceed1MiB. Caller actual
ReadAt/outerCRC/RID/owner identity and downstream template custody remain gomap.

Each independently interpreted entropy/table probe/token/control consumes R;
actual copies, zeroing, table reads/writes, expansion and hash consume B before
effects. Refusal preserves original partial parse/copy state. Cold predefined
and dictionary tables are phased, with no global/pool warming. Conservative
allocator admission follows gomap's original retainedalloc policy, rounded per
separate allocation. Fresh constructor requests58472B, admits66048B, and observes
65824B in2 allocations on explicit Go1.26.4linux/amd64. Returned Decoder remains
caller-reachable after CloseStep; its512B admitted fixed object is released only
by enclosing last-owner retirement. Close is logical alias cleanup, not physical
file/definition/resource death. `zstd/BOUNDED_DECODER.md` is the full contract.

## Semantic and accounting evidence

`raw/freeze-normal.log` records final focused actual-producer/dictionary/framing,
refusal/reset/cancel/overlap/custody/allocation controls and phase R/B. Full
ordinary affected normal and combined noasm+nounsafe suites are retained in
`raw/coherent-normal.log` and `raw/coherent-noasm-nounsafe.log`; subsequent edits
only refine bounded reservations/static names/cleanup and test vet safety.
`raw/freeze-race.log`, `raw/freeze-386-safe.log`, and `raw/freeze-vet.log` bind final
risk checks. Windowsamd64 and Darwinarm64 compile controls are in
`raw/coherent-platform-compile.log`; these prove compilation, not runtime.
Existing GitHub matrix remains required on the PR's exact head.

Actual gomap255-leaf complete/compact/dictionary Writer frames and >2MiB pointed
frame are checked against original ordinary decoding. Exact production fixture
bytes/settings/source/hashes are in `zstd/testdata/bounded-gomap/receipts.json`,
`producer-leaf-receipts.json`, `producer-pointed-census.json`, and the two exact
harness sources in `producer-harness/` (suffixed `.go.txt` so artifact snapshots
cannot become extra Go packages). Producer command recipes are embedded in the
retained raw logs/source; set `NATIVE_ZSTD_LEAF_ARTIFACT` or
`NATIVE_ZSTD_RANK_ARTIFACT` and `NATIVE_ZSTD_RANK_EXPORT=1`, using frozen gomap
source and explicit Go1.26.4/GOWORKoff/GOMAXPROCS2/-p2. CRC alteration is rejected
by ordinary selected-leaf/pointed semantics before export.

Final actual rich caller-fed Spans rank exceeds36000 Q32 calls; per-case actual
R/B/calls, caller reads, and conservative peak are in `raw/freeze-normal.log`.
The N/2N witness has no growing output collector in its measured decoder interval;
input/oracle/spans are prepared outside it, with their exclusion stated. Linked
history lookup scales with retained segments and is never labeled O(1).
Malformed dictionary/header/block/literal error paths allocate0 per operation;
package-static returned errors do not authorize capacity refunds. Final custody
tests check actual Huffman prevTable/dt aliases before refund, and cleanup can
pause at1R without releasing surviving edges.

The original BuildDictWorkspace RED reproduced on exact87f. The existing offset
comparator tested distinct key equality instead of equal histogram frequency;
minimal frequency comparison restores its documented descending-offset tie-break.
Original and characterized tests are retained, without decoder rollback or test
weakening. Early parser/table/zero-bit/dictionary-selection/fixture/vet failures
remain raw diagnostics, not hidden passing evidence.

## Ordinary cost applicability

Quiet serial five-repeat100x matched controls use the same overlay and actual
leaf/rich payloads, timing ordinary DecodeAll reuse, stream reuse, and
construct+decode+Close. Root independently verified all6x5 parsed observations.
Medians head/base range0.9722--1.01224; reuse B/op/allocs unchanged; constructor
fixed Decoder field adds30/32B/op with the same12/18 allocations. This is focused
ordinary guardrail evidence, not native throughput acceptance. Exact measured
engine bytes are recoverable under `cost-source/`; hashes and exact current-vs-
measured deltas are in `ordinary-source-applicability.json` and
`cost-source-diffs/`. Current whole bounded source is not falsely labeled byte-
identical to the cost snapshot. Ordinary caller/assembly/format algorithms are
unchanged by post-cost edits: shared nil-work grammar/formatted errors remain;
rounding is applied only with nonnil bounded Work, static names change no valid
ordinary result, and charged reset/detach/cleanup effects do not alter nil-work
synchronous semantics. No additional throughput claim is inferred from this.
Fresh ordinary baseline leaf and rich DecodeAll allocations expose whole-output
allocation aboveQ1MiB; those genuine RED observations remain in raw files.

## Remaining owning-node gates and release

The protected exact original SAME Request33/80/4096 fixture's first-ACK rank
remains OPEN until native current-output/cursor work ABI and its real write-phase
schedule exist. `exact-original-codec-rank-preflight.md` records exact fixture,
ledger and missing-source hashes; no rich frame substitutes for it. Native all-
codec/template/root/file/publication/physical Finish/caps/all19/fault/performance
and power-loss gates remain their original gomap owners' responsibilities. Fork
review and latest-head matrix are separate remaining #5129 acceptance gates.

`base-control-release/receipt.json` records exact pinned-base WT release,
47,058,406 measured file bytes and48,484,352 observed ambient free-space delta
separately. Active fork/native WTs and shared caches remain protected. Attributable
producer outputs and platform binaries may be released only after final fixture
consumers finish and exact hashes/source recipes/raw evidence are durable.
