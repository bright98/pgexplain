// Package rules provides built-in advisor rules for pgexplain.
//
// Each rule implements the [advisor.Rule] interface and is constructed via a
// factory function (e.g. [SeqScan], [SortSpill]). Rules accept optional
// functional options to override default thresholds.
//
// # Built-in rules
//
//   - [SeqScan]               — sequential scan on a large table
//   - [RowEstimateMismatch]   — plan row estimate far from actual rows
//   - [HashJoinSpill]         — hash join batch count > 1 (disk spill)
//   - [NestedLoopLarge]       — nested-loop join over a large outer side
//   - [MissingIndexOnlyScan]  — index-only scan with high heap-fetch ratio
//   - [SortSpill]             — sort spilled to disk (external merge)
//   - [TopNHeapsort]          — top-N heapsort over a full sequential scan
//   - [ParallelNotLaunched]   — fewer parallel workers launched than planned
//
// All rules are safe for concurrent use.
package rules
