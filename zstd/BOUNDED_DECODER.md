# Bounded original Zstandard decoding

`NewBoundedReader` constructs a single-owner continuation in the existing
`Decoder`. It shares the original frame-header grammar, FSE normalization and
spread, Huffman table construction, and scalar sequence decoder. Ordinary
`NewReader`, `DecodeAll`, streaming, dictionary copying, and assembly fast paths
retain their contracts. No ordinary whole-decode fallback runs in a bounded
step, and no worker, file owner, dictionary registry, or output cache is added.

Ordinary FSE normalization/table construction, Huffman `ReadTable`, public frame
header parsing, and scalar sequence decoding keep their direct optimized loops.
Only explicitly caller-budgeted operations enter the suspended state machines;
ordinary calls do not pay per-symbol continuation dispatch or callback overhead.
The bounded states preserve the same format grammar and reuse the original
bitreaders, tables, scratch, and scalar sequence primitives. Differential corpus
and produced-frame checks protect the two entry routes.

## Work and admission

Supply one `DecoderWork` belonging to the enclosing operation. `Reserve(R, B)`
atomically debits its existing record and byte operands before effects; refusal
changes neither operand. `Available` reads those same operands. Callbacks must
perform no uncharged allocation, lookup, I/O, background work, or waiting.
`Retain` separately admits retained capacity before allocation. Work already
reserved is not refunded if capacity admission subsequently refuses.

The continuation prices each independently interpreted entropy/table probe,
token, or control transition as record work. Copies, zeroing, table reads and
writes, expansion, and checksum effects also debit bytes. Fixed small grammar
transitions may reserve several records together when all of those actions are
indivisible; a block or table is never priced as one record. Huffman output
literals each debit their actual table probe. Table builders and backward
history lookup preserve their exact suspended entry/node. Partial copies and
expansion survive refusal; consumed work is not replayed on the next call.
Input/output/checksum chunks are at most 4096 bytes. Frames without a content
checksum perform and charge only their actual output/history copies; they do not
initialize or update checksum state. Checksummed frames retain their original
scan/carry admission and final integrity check. Cold predefined tables are built
through the same phased original table builder, without a global warmup.

Decoder-owned allocations use the existing conservative gomap COW allocation
policy, implemented in `internal/decoderwork.AllocationCharge`. Each allocation
is rounded separately: small containing objects use power-of-two envelopes
starting at 16 bytes with the pointerful-header allowance; larger allocations
round to 8192 bytes. Overflow fails closed. Requested/addressable lengths,
conservative admission, and observed runtime allocation are different numbers.
For example, Go 1.26.4 linux/amd64 currently requests 264 bytes for `Decoder` and
58208 for its continuation, admits 512 + 65536 = 66048, and the fresh-process
witness observes 65824 allocated bytes in two allocations. No process-total or
rounded runtime statistic is substituted for the admission contract.

The caller must independently admit complete containing capacities of borrowed
input, dictionary definitions, Raw output, and I/O/output spans. Slice length is
not a capacity discount. The library accounts only its own allocations and
actual decode effects; it cannot certify the caller's allocation or retirement.
An external owner can charge already-owned immutable backing as a borrow, but
cannot hide a cold whole-frame make/read behind that name.

## Input and output

Choose one of two input forms:

* `Begin(input, owner, mode, raw, dictionary)` borrows genuinely immutable,
  admitted complete input and the existing owner. No input copy is prepared.
* `BeginStream(owner, mode, raw, dictionary)` admits one original maximum block
  plus maximum header wire buffer: 128 KiB + 18 requested bytes. It performs no
  I/O. After `Step` pauses with `InputRequest`, supply wire bytes through
  `FeedInput`. The returned consumed length may be smaller than the supplied
  span; carry the remainder forward without restarting the read. Each copy is
  charged before effects and at most 4096 bytes. No supplied span alias escapes
  `FeedInput`. Set `final` only when the supplied bytes end the exact immutable
  input. A zero-byte final feed marks EOF. Block/parser aliases are detached
  before wire-buffer compaction or reuse.

The stream accepts the original header, skippable, block, checksum, multi-frame,
and tail grammar, including uint32 skippable lengths. The original maximum
compressed block is 128 KiB; frames and pointed values can be much larger. The
caller prices its actual `ReadAt`, outer record directory/RID/CRC, immutable
file identity, and lifetime before supplying bytes. The decoder neither opens
files nor proves an enclosing value-log checksum.

`BoundedRaw` uses complete caller-admitted output as the original synchronous
history. `RawResult` is available only after successful completion, and returns
a borrow of that output. This is suitable for an actual complete external-leaf
group, whose existing envelope is at most 255 * 4096 decoded bytes. It does not
allocate an alternate Raw destination. Requested complete output is also valid
for other frames when the caller has honestly admitted it.

`BoundedSpans` writes into a fresh caller-admitted span passed to `Step`; it never
retains that span. It stores necessary original history in separately admitted
32 KiB payload segments inside the existing history abstraction. Each requested
32800-byte linux/amd64 containing node admits 40960 bytes. Match overlap and
segment boundaries use the original scalar copy semantics. Lookup walks
backward through the same linked history and prices each probe; it is not a
constant-time index. Old history retires between blocks only after suspended
match aliases are gone, with the real frame window plus current-block overlap.
A single-segment frame can require a window equal to the entire decoded value.
That capacity is real, even though no second complete output destination exists.
The caller must accept/refuse that actual retention through its original cap;
there is no always-small-window promise.

A span is tentative output until the entire frame/input succeeds. Corrupt late
checksum, truncated tail, size mismatch, or enclosing CRC failure cannot publish
an earlier emitted prefix. A downstream grammar may need to retain emitted bytes
(for example a complete template mask); the caller must admit that custody. The
verification collectors in tests are oracle infrastructure, excluded from the
bounded operation, and do not qualify native integrated resource limits.

## Explicit output and window limits

`NewBoundedReaderWithLimits(work, maxDecodedSize, maxWindowSize)` uses the same
continuation with explicit `DecodeAll` limits. The decoded-size domain is
1 through `1<<63`; the window domain is `MinWindowSize` through
`(1<<41)+7*(1<<38)`, matching `WithDecoderMaxMemory` and
`WithDecoderMaxWindow`. Invalid configuration returns the static
`ErrDecoderSizeExceeded` or `ErrWindowSizeExceeded` before work or retention
admission. Nil work returns `ErrBoundedMode`. Valid limits do not allocate a
window eagerly or admit any caller-owned backing.

Every non-skippable frame passes the original suspended header parser. As in
`frameDec.reset` and `DecodeAll`, the advertised window must not exceed either
limit; a single-segment frame then uses at least the 1KiB minimum window, which
must fit the memory limit and the configured window. A known content size must
fit the remaining decoded allowance. Output with unknown content size is also
checked before each admitted copy. Total output includes all concatenated
frames, while window and content-size integrity remain per-frame. Skippable
frames consume work and input but no decoded allowance. CRC, dictionary,
block/history, truncation, and output-capacity checks remain active.

These rules apply equally to Raw, Spans, and caller-fed input. In particular,
fed input still uses the aggregate `DecodeAll` output limit: it does not adopt
the ordinary `io.Reader` streaming interpretation of the memory option.
`ResetStep` and `ResetStreamStep` start a fresh decoded allowance but preserve
the configured limits. All failure and cancellation paths use the existing
`CloseStep` retirement contract. The two fixed limit fields are included in
the continuation's existing `unsafe.Sizeof` admission and release; there is no
new callback, owner, pool, ordinary decode fallback, or frame pre-parser.

`NewBoundedReader(work)` keeps its original policy: the existing default
advertised-window ceiling and per-frame 64GiB decoded ceiling, without imposing
a new aggregate output limit. Its continuation admission includes the added
fixed fields, just as it includes all other control fields. No existing caller
silently receives the new policy. Explicit limit errors preserve the ordinary
size/window error classes; invalid constructor arguments use the static errors
above rather than the ordinary options' formatted configuration diagnostics.

The limit tests compare actual bounded output/error classes against ordinary
`DecodeAll`, including later concatenated frames, unknown content sizes,
skippables, CRC failures, borrowed dictionaries, reset after success/failure,
and both output/input modes. The new API cannot be called by the unmodified
baseline; compilation against a missing constructor is not a behavioral RED.
The ordinary public API supplies the candidate's behavioral oracle.

## Dictionary and lifetime

`BorrowedDictionary` binds one complete immutable raw or binary definition to
its existing owner. Binary definitions are validated in suspended original
Huffman/FSE/offset phases. No eager ordinary `WithDecoderDicts` copy is hidden
in construction. Raw dictionary ID zero preserves ordinary dictionary-zero
selection; explicitly empty raw definitions remain valid. A nonzero binary ID
without complete definition bytes returns `ErrUnknownDictionary`; an ID alone
cannot install binary Huffman/FSE tables or offsets. A different supplied ID
cannot satisfy an unselected dictionary.
The original ordinary APIs continue to copy definitions as before.

Every input, dictionary, Raw, file, and definition owner remains admitted while
any dependent decoder alias exists. The decoder is single-threaded; do not mix
bounded operations with ordinary Read/Reset/WriteTo/DecodeAll. Those APIs report
`ErrBoundedMode`. Ordinary `Close` cannot perform bounded cleanup and does not
retire a bounded continuation; use `CloseStep` until it returns true.

`CloseStep` is cancellation after any phase or error. It clears partial parser,
bitreader, copy, table, history, input, output, dictionary, and owner aliases in
charged phases before releasing owned capacity. Static bounded errors allocate
no per-operation diagnostic object. Ordinary nil-work branches retain existing
formatted error diagnostics. A still-returned error is not a refund token.

`ResetStep` / `ResetStreamStep` retire old aliases and backing through the same
cleanup continuation, then bind the new immutable owner. Retry with stable new
arguments. They preserve the same already-constructed predefined tables and
fixed continuation; those are neither rebuilt nor readmitted. A cold decoder
pays cold work. There is no pooled/global preparation control.

`CloseStep` detaches and releases the continuation, but the returned `*Decoder`
remains caller-reachable. `BoundedControlSize()` reports its conservative fixed
allocation admission (512 bytes in the witness above). Only the enclosing last
owner can drop its final Decoder edge and release that capacity. Likewise,
logical Close never proves physical file/definition death, nor releases another
owner's Raw output or resource callbacks. Native root/workspace retirement
remains the existing enclosing authority.

## Frame reuse with an unchanged validated dictionary

`ResetFrameStep(input, owner, BoundedRaw, raw)` explicitly detaches the prior
frame's input, output, parser and history aliases while retaining the SAME
validated dictionary definition, owner, tables and admitted reusable literal and
Huffman backing. It takes no dictionary argument, compares no identity cache,
and transfers no dictionary owner. That original definition and owner must remain
immutable and admitted until full reset or terminal Close completes. Its exact
embedded dictionary ID remains checked by every decoded frame.

Cold/incomplete or failed dictionary preparation, nil new owner, unsupported
output mode, terminal Close and an in-progress full reset reject before effects.
A failed frame can be reused after its dictionary context was validated; its
frame error and output allowance are reset. The method is resumable with stable
new operands, like full reset. A successful call binds the new frame and proves
all prior borrowed frame aliases are detached. Empty input/Raw can deliberately
leave no frame-byte aliases between operations; its new owner still applies.

This uses the original cleanup phases and existing Huffman/FSE `DetachInput`
helpers. It keeps dictionary semantics but clears frame entropy/history
permission, so a previous frame cannot authorize treeless literals or repeat
sequences. Existing limits and all block/dictionary/checksum checks remain.
Repeated identical warmed Raw frames require no new `Retain`; a different frame
can require larger scratch and must admit it normally. This promises neither
zero work nor zero allocation for arbitrary future frames. Owned caller input,
Raw and dictionary capacities remain independently admitted.

Only Raw target output is supported initially. Prior segmented history still
retires through its existing cleanup; there is no segmented-history retention
project or zero-allocation Spans promise. Caller-fed wire backing also retires;
this method binds complete input, not a new streaming operation.

Original `ResetStep` / `ResetStreamStep` continue to detach the definition and
retire all scratch. Either full reset or Close promotes a paused frame reset by
restarting the SAME cleanup phases, including cohorts previously preserved by
frame reuse. Reuse cannot downgrade that intent. The new fixed latch is charged
through the existing continuation `unsafe.Sizeof` admission and release. No pool,
additional owner, generic fallback, frame pre-parser or admission discount exists.

## Scope of evidence

Tests include immutable frames from actual gomap `Writer.AppendFrameWithStats`
and original maximum external-leaf producer frames, selected-leaf/whole-decode
oracles, dictionary definitions, charged file reads/feed copies, cold and reused
construction, overlap/refusal/cancellation, linked-history probes, all original
framing, late corruption, and N/2N retained/allocation observations. The fixture
recipe is `testdata/bounded-gomap/receipts.json`, pinned to gomap
075ae77487778a872384a935c6d550359ca76452 and compression producer
87fb149e472104f0c2fa7a961b2493c4372603dc. Original DecodeAll remains the semantic
oracle, not the bounded-work implementation.

The actual entropy-rich 2101248-byte pointed frame needs more than 36000 Q32
calls with conservative independent-probe pricing. This is its truthful rank,
not a universal 4096-call claim. The protected native SAME Request33/80/4096
first-ACK fixture has its own unchanged exact input and phase schedule; a fork
cannot substitute this rich frame for it. Its integrated rank, outer format/CRC,
template handling, all-codec projection, file/root custody, and native publication
and retirement gates remain gomap #4878 work. This component changes no wire
format and does not activate native pruning or qualify power-loss durability.
