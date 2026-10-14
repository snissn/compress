# BeginStream after paused original CloseStep

The public BeginStream now rejects a closing decoder before any reservation, retention, or input-buffer allocation. It uses the same guard as Begin. Original close phases and refund rules are unchanged.

The regression uses public NewBoundedReader/CloseStep/BeginStream/Begin and the real admission callback to pause unbegun decoders at close phase0 or6 in Raw and Spans modes. It observes real state without mutating private lifecycle fields. Final exact-original-c33 overlay is RED in all four cases: rejected calls allocate139264 bytes; late-phase Close strands that capacity. The same final test with the guard is GREEN, rejected calls perform no work or retain changes, and phased Close plus containing control retirement reaches zero.

Final allbounded normal, focused race and386 safe controls pass. Early test-harness failures are retained; commands.json distinguishes the final actual original-source RED and final GREEN. The overlay's two exact files/root were physically removed, verified by baseline-overlay-cleanup.json. source.json binds the one-line production delta and unchanged prior packet members. Prior ordinary/full decoder measurements remain historical, byte-applicable evidence; current exact-head GitHub CI is a separate merge gate. No native whole-prune gate is claimed complete.
