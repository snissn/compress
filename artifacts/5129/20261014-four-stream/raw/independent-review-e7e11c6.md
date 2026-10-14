review_mode: INTERNAL_SUBAGENT_REVIEW
reviewer: /root/mvcc_compress5_independent_review_sol61
requested_model: gpt-6.1-sol
requested_effort: high
served_model: unknown (not observable from reviewer tools)
base_sha: 87fb149e472104f0c2fa7a961b2493c4372603dc
head_sha: e7e11c6fc2695eec0c4d127a6bda83ceedc475d8
disposition: REJECT
candidate_edits_by_reviewer: false
hosted_unavailability_evidence: coordinator-owned; not independently inventoried or asserted here

Independent read-only exact-candidate review of snissn/compress PR 5, component prerequisite gomap #5129 for #4878. No construction role, delegation, repository/GitHub edits, hosted triggers, or merge. Read github-pr-mergeable/SKILL.md, references/internal-review-fallback.md, and executor model/concurrency rules. Coordinator retains CI/readiness/merge ownership.

Policy audit: recursively enumerated exact-head tracked tree. No tracked AGENTS.md, CONTRIBUTING document, PR template, or merge-policy file found. The supplied gomap AGENTS instructions were treated as parent workstream constraints; this review does not authorize native qualification. Source review used git archive/show/diff of the immutable head and intended base, extracted only outside the WT at /tmp/compress5-independent-e7e11c6. Remote WT HEAD matched the candidate; final git status --porcelain was empty.

Findings:

1. P1, blocking correctness/corruption handling: huff0/decompress_step.go:321-328 and :346. A malformed four-stream literal block with a short declared regenerated size reaches an out-of-range destination offset and panics, whereas ordinary DecodeAll reports corruption. For output length 1, each=(1+3)/4=1. After a one-symbol stream 0 and empty stream 1, stream 2 starts at offset 2, with limit=min(3,1)=1. Phase 4 tests only offset==limit, so it proceeds through a valid one-bit lookup to dst[2] with len(dst)==1. The malformed wire can therefore crash callers of the public bounded decoder. This violates the required corrupt-input behavior and is not a native-integration issue.

Public witness frame, 25 bytes:
28b52ffd200185000016000380100100010001000201020100

The frame has FCS=1, last compressed block stored size=16, four-stream compressed literals regenerated size=1, compressed literal size=12, a valid two-symbol Huffman table, three one-byte jump lengths, and stream payloads 02/01/02/01. The trailing sequence count is zero. No internal fields or synthetic parser state are changed in the witness.

Local witness: /tmp/compress5-malformed-four.go
Remote witness: /tmp/compress5-malformed-four-e7e11c6.go
Witness SHA256: 05af3d619d7f1e3b2cc0e37979845fce0b81cc30e5e8ab0169f9a4537419f8fc

Executed remote command:
cd /home/mikers/benchmarks/mvcc-4871-repair-20261013/compress-5129
GOWORK=off GOMAXPROCS=2 GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local /home/mikers/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.4.linux-amd64/bin/go run -p 2 /tmp/compress5-malformed-four-e7e11c6.go

Actual stdout:
ordinary= err=corruption detected: stream overrun 4
bounded panic=runtime error: index out of range [2] with length 1

The witness calls ordinary NewReader/DecodeAll/Close, then NewBoundedReader/Begin/Step with a Q32/1MiB work callback, resetting those same operands per Step. recover catches the panic for observation. Oracle/output infrastructure is outside a measured cost claim. The witness is not proof of native caps or performance.

Suggested repair: validate every four-stream destination partition before interpretation and return an existing/static corruption error for impossible partitions. Preserve valid four-stream behavior, pre-effect accounting, and refusal state. Add public whole/fed Raw/Spans malformed controls for short destinations (including lengths 1, 2, and 5 where applicable), plus cancellation cleanup after the error. Changing equality to a skip condition alone should not silently accept invalid stream/output partitions. Disposition remains open; reviewer did not fix source.

Reviewed paths and claims:
- zstd/decoder_bounded.go in full: constructor allocation/admission, Begin/BeginStream, caller-feed copies/compaction, raw/spans overlap, cold tables, dictionary grammar/selection, block/literal/sequence phases, R/B charging, history lookup, output/copy/hash, CRC/FCS/skippable/concatenated frames, reset/cancel/close and surviving fixed Decoder admission.
- internal/decoderwork/{work,allocation,ncount,table}.go: atomic callback contract, separately rounded containing allocations, overflow fail-closed behavior, suspended normalization/spread/next-state probes and static bounded versus formatted ordinary errors.
- fse/{bitreader,decompress,decompress_step}.go; huff0/{bitreader,decompress,decompress_step}.go; zstd/{bitreader,decodeheader,decodeheader_step,blockdec,fse_decoder,fse_decoder_generic,fse_predefined,seqdec,seqdec_step,history,history_step,decoder,dict}.go: shared grammar/scalar engines, nil-work ordinary routes, existing assembly route retention, dictionary tie-break correction, alias removal before storage refunds.
- zstd/BOUNDED_DECODER.md, zstd/README.md, bounded tests including producer/input/lifecycle/dictionary-zero/allocation/ordinary-control/multiframe tests, and Huffman custody test. Documentation is appropriately limited to component construction and caller responsibilities, except corrupt-input coverage is insufficient for finding 1.
- .github/workflows/go.yml, huff0/_generate/go.mod, zstd/_generate/go.mod, s2sx.mod, s2/cmd/_s2sx/go.mod: repair paths correctly name the fork in actual existing module/build/generator consumers; no new file/registry/publisher authority.
- artifacts/5129/20261014 and 20261014-ci-review: source manifests, repair diff/receipts, raw controls, producer recipes/fixture receipts, ordinary cost comparison/applicability and retained source/diffs.

Checks actually performed:
- Immutable intended-base/candidate diff and complete production continuation/helper source inspections listed above, recursive policy enumeration, source/workingtree SHA verification. No normal/race/full-suite reruns.
- Original packet manifest SHA256 b7e99b423e7d7a15bcbaf70c9de8661716a39d61dd900d4fb3e938b3197c79f5 verified; all 120 listed member size/hash receipts were available and SHA256 matched.
- Repair packet manifest SHA256 03f4f60d517b6bc949f9d4ee8f33ada64405c82c2a2f3a4fc1e33f8f78425f05 verified; all 29 member hashes matched. source.json SHA256 fde0438601f5a1af8ddfd5fcebf59969aebd7af6b65c3d00156ce1b15f9529ad is bound by that manifest; all seven named current source hashes matched.
- Original source-manifest has 45 entries. All extracted entries matched except decoder_bounded.go, which is explicitly rebound by repair source receipt. go.sum/LICENSE were checked by immutable git show remotely and matched their source-manifest hashes. Final affected source hashes: huff0/decompress_step.go=1a3edd5d4fa49e8b9f9ade2d57c3264b452ef68f5c5e862d291d2e0f0275e2af; zstd/decoder_bounded.go=c47ecd554af5741df795ac7726bd3cf2b3f0c53a04886a6787af66b25085d6d9.
- All actual linked bounded-gomap payloads matched recipes: leaf255-full.zstd, leaf255-compact.zstd, pointed-rich.zstd, leaf255-dictionary.zstd, leaf255-dictionary.dict. Reviewed pinned producer sources/settings/export recipes and raw provenance. Did not regenerate producer payloads.
- git diff 99a8f74436c977080236326f2cdd40ef39d53107..candidate confirms bounded phase18 frame-relative lookup is the only engine change; remaining changes are recorded module/workflow corrections and new multiframe tests/artifacts. The repair matches segmented frame coordinates and avoids prior-frame authorization. New tests cover Raw/Spans, whole/fed, dictionary selection/reset and real second-frame match cancellation.
- Public cold-close reentry witness /tmp/compress5-close-reentry-e7e11c6.go did NOT reproduce a lifecycle issue: begun guard rejects BeginStream during paused close and final retention is exactly BoundedControlSize=512. That suspicion is rejected, not a finding.
- Public malformed four-stream witness above reproduced the blocking issue at clean exact candidate; final HEAD and source hashes were rechecked after execution.

Evidence applicability and gaps:
- Existing normal/noasm+nounsafe/race/386/vet/platform-compile and final multiframe logs remain valid receipts for their recorded tested scopes. They do not exercise the malformed four-stream short-output input above. No independent broad-suite rerun is claimed.
- Ordinary cost receipts contain focused matched six-case controls with five repeats; reuse allocations are unchanged; constructor bytes have the documented 30/32-byte increase. These are guardrails, not native throughput acceptance. Retained measured sources and later deltas are explicit; current whole bounded source is not byte-identical to the cost snapshot. The final repair affects bounded phase18 only, so it does not by itself invalidate ordinary-cost evidence. No new noasm/dictionary-wide cost claim is made.
- Constructor admits Decoder and continuation separately before make, with fixed sizes that cannot induce a runtime-controlled summed overflow. Other allocations have bounded format constants/table logs and use conservative AllocationCharge. Cleanup source removes parser, table, history, input, and output aliases before refunds on valid documented lifecycles; fixed returned Decoder admission remains caller-owned after CloseStep.
- Public documentation truthfully keeps caller whole input/output/dictionary backing admission, actual I/O, physical owner retirement, and downstream tentative-output custody separate. Segmented history may retain a whole single-segment frame and linked lookup is charged, not represented as O(1).
- Protected original SAMERequest33/80/4096 first-ACK rank/integration, file/resource caps, outer CRC/template/native Finish, all19 gates, publication and power-loss remain explicitly unqualified. This review grants no authority over those gates or hosted CI/review-thread inventory.

No further blocking finding was established in this bounded reviewer pass. Simplification judgment: shared existing Header/FSE/Huffman/scalar continuation mechanisms avoid a separate whole-decode engine; no safe required behavior reduction is recommended. Repair finding 1 in one coherent batch and renew exact-candidate review with focused malformed-input and sibling valid-stream/cancellation controls. CI and merge remain coordinator-owned.
