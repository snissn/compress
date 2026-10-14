# Ordinary optimized entry routes

Actual hosted review at 3b25800b found P1 regressions in ordinary FSE normalization/table construction and Huffman ReadTable. Restore both direct optimized loops and the analogous Zstd normalization/generic table/public-header routes byte-exact to maintained upstream87fb. Explicit bounded states, work/retention pricing, producer bytes, dictionaries, lifecycle repair and ordinary scalar sequence route remain unchanged. Ordinary Scratch representations are also byte-exact upstream.

Actual findings: https://github.com/snissn/compress/pull/5#discussion_r4269725774 and https://github.com/snissn/compress/pull/5#discussion_r4269725784.

## Focused cost evidence

Existing tracked benchmark harnesses were not edited. Same Go1.26.4 linux/amd64, GOWORKoff, GOMAXPROCS2, seven fixed 10000x samples/case, same producer/setup and scratch reuse; medians below. Commands/environment and source identities are bound in commands.json, before-source.json and candidate-source.json. This is short component evidence on a shared host, not quiet final Dgraph M0 evidence. Original FSE harness validates exact bytes before ResetTimer; Huffman harness validates codec errors during the timed loop and existing full correctness tests cover decoded bytes. Compression/setup is excluded. All cases zero allocations.

| Existing case | Before ns/op | After ns/op | Before ops/sec | After ops/sec | Change | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|---:|---:|
| fse/gettysburg-2 | 4746.00 | 3345.00 | 210704 | 298954 | -29.52% | 0 | 0 |
| fse/endzerobits-2 | 230.00 | 86.58 | 4347826 | 11550012 | -62.36% | 0 | 0 |
| fse/normcount2-2 | 1529.00 | 589.10 | 654022 | 1697505 | -61.47% | 0 | 0 |
| huff/crash2-2 | 574.70 | 323.30 | 1740038 | 3093102 | -43.74% | 0 | 0 |
| huff/endnonzero-2 | 412.40 | 233.50 | 2424830 | 4282655 | -43.38% | 0 | 0 |
| huff/case1-2 | 1547.00 | 948.10 | 646412 | 1054741 | -38.71% | 0 | 0 |

## Validation and retained applicability

Full ordinary fse/huff tests, focused bounded matrix, nounsafe race, noasm+nounsafe386 matrix, vet, final Header golden corpus+all bounded Zstd tests pass. Raw logs and exact executed commands are retained; no harness/fixture/rank/cap workaround was introduced. The only remaining ordinary nil-work bitreader branches select corruption error formatting, outside normal decode loops.

source.json binds all165 production/assembly objects, six producers and every prior artifact member. All changed ordinary production files are byte-exact upstream; all bounded engines are unchanged. Existing prior full-source and delta reviews retain their original scope; affected exact-candidate independent review/currentCI, actual thread disposition, merge and gomap version binding remain separate. No native MVCC, outer CRC/Finish, original SAMERequest33/80/4096, all19, durability or parent performance gate is accepted by these component results.
