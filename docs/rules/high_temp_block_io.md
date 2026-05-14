# Rule: HighTempBlockIO

**Package:** `rules`  
**Constructor:** `rules.HighTempBlockIO()`  
**Severity:** `Warn`

---

## What is temporary block I/O?

When PostgreSQL executes an operation that needs to hold intermediate rows in memory — a GROUP BY aggregation, a window function, a materialized CTE, or a subquery result — it draws from **`work_mem`**, a per-operation memory budget. When the working set exceeds that budget, PostgreSQL cannot keep the data in RAM and must write it to **temporary files** on disk.

These temporary writes and reads are tracked per node in EXPLAIN output (when the `BUFFERS` option is used):

| Field | Meaning |
|---|---|
| `Temp Written Blocks` | Disk blocks written to temporary files |
| `Temp Read Blocks` | Disk blocks read back from temporary files |

One block is 8kB (the default PostgreSQL page size). In a typical spill, the two counts are equal: every block written to disk must be read back before the node can complete. A `HashAggregate` with `Temp Written Blocks: 9800` means PostgreSQL wrote and later re-read 9,800 × 8kB = ~77MB of otherwise-avoidable disk I/O.

---

## Why does it matter?

Temporary disk I/O is sequential — not random — but it is still **dramatically slower than in-memory execution**:

- Every byte written must be read back at least once, so the I/O cost is effectively doubled.
- Temporary files compete with regular table I/O for disk bandwidth, slowing other concurrent queries.
- PostgreSQL cannot pipeline the result through the node until the full temp file is re-read; this increases query latency in proportion to the spill size.

Unlike sort spills (Sort Space Type: Disk) and hash join spills (Hash Batches > 1), which have dedicated rules with node-specific diagnostics, **many other node types can also spill** — most commonly `HashAggregate` (GROUP BY / DISTINCT), `WindowAgg` (window functions), `SubqueryScan`, and materialized CTEs. This rule catches all of them.

---

## The critical detail

**These fields require `BUFFERS`.** `Temp Written Blocks` and `Temp Read Blocks` are only populated when EXPLAIN is run with both `ANALYZE` and `BUFFERS`:

```sql
EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) <query>;
```

Without `BUFFERS`, both fields are absent from the JSON and the rule stays silent — even if the node spilled heavily. The rule does not fire when both fields are nil.

---

## Common causes

| Cause | Explanation |
|---|---|
| `work_mem` too small for the data | Most common. The default is 4MB — a GROUP BY on a table with many distinct keys easily exceeds this |
| Large DISTINCT or GROUP BY over unindexed columns | Forces a full hash aggregation with no early pruning |
| Window functions over large partitions | Each partition is buffered in memory; large partitions spill |
| Materialized CTEs scanned multiple times | PostgreSQL materializes the CTE once; if it exceeds `work_mem`, it spills to disk for every subsequent scan |
| High parallelism increasing per-worker load | More workers = smaller slice per worker, but also more `work_mem` consumers simultaneously |
| Stale statistics causing wrong plan | Planner chose HashAggregate expecting few distinct groups; actual cardinality was much higher |

---

## What does this rule check?

```
IF node is not a Sort or Hash node
AND TempWrittenBlocks is non-nil OR TempReadBlocks is non-nil
AND max(TempWrittenBlocks, TempReadBlocks) >= minTempBlocks  (default: 256)
→ emit Warn finding
```

Sort and Hash nodes are explicitly excluded because `SortSpill` and `HashJoinSpill` already cover them with richer, node-specific diagnostics. `HighTempBlockIO` complements those rules by covering every other node type.

**Fields read:**

| Field | Source | Meaning |
|---|---|---|
| `TempWrittenBlocks` | Node (BUFFERS) | Blocks written to temp files; nil if BUFFERS not used |
| `TempReadBlocks` | Node (BUFFERS) | Blocks read from temp files; nil if BUFFERS not used |
| `NodeType` | Node | Used in finding message; Sort and Hash are skipped |

**ANALYZE + BUFFERS requirement:** Both are needed. `ANALYZE` alone does not populate block I/O counters.

---

## How to use it

```go
import (
    "github.com/bright98/pgexplain/advisor"
    "github.com/bright98/pgexplain/parser"
    "github.com/bright98/pgexplain/rules"
)

plan, _ := parser.Parse(explainJSON)

adv := advisor.New(
    rules.HighTempBlockIO(),
)

for _, f := range adv.Analyze(plan) {
    fmt.Printf("[%s] %s\n", f.Severity, f.Message)
    fmt.Printf("  detail:     %s\n", f.Detail)
    fmt.Printf("  suggestion: %s\n\n", f.Suggestion)
}
```

---

## Options

### `WithMinTempBlocks(n int64)`

**Default:** `256` (≈ 2MB at 8kB/block)

Sets the minimum block count — using the larger of `TempWrittenBlocks` and `TempReadBlocks` — required to emit a finding.

**Usage variants:**

```go
// Default: fire when temp I/O >= 256 blocks (≈ 2MB)
rules.HighTempBlockIO()

// More sensitive: fire on any temp I/O (catches even small spills)
rules.HighTempBlockIO(rules.WithMinTempBlocks(1))

// Less sensitive: only flag when temp I/O >= 8MB
rules.HighTempBlockIO(rules.WithMinTempBlocks(1024))

// Roughly match work_mem default (4MB = 512 blocks)
rules.HighTempBlockIO(rules.WithMinTempBlocks(512))
```

**Guidance on choosing a value:**

- `1` is useful for strict auditing — any spill at all is worth knowing about.
- `256` (default, ≈ 2MB) avoids noise from trivial spills while catching anything meaningful at the default `work_mem` of 4MB.
- Match the threshold to your `work_mem` setting: if `work_mem = 64MB`, a spill of 100 blocks is proportionally tiny. Set the threshold to something like `2 × work_mem_in_blocks` so only significant spills fire.

---

## Sample finding

For a `HashAggregate` node with 9,800 temp blocks written and read:

```
Severity:   WARN
NodeID:     1
NodeType:   HashAggregate

Message:    HashAggregate spilled to disk using 9800 temp blocks (≈ 77MB)

Detail:     This HashAggregate node wrote intermediate results to temporary disk files
            because its working set exceeded work_mem. Each block written to disk must
            be read back before the node completes, adding sequential I/O that in-memory
            execution avoids. Temp blocks written: 9800, read: 9800 (≈ 77MB).

Suggestion: Increase work_mem to at least 77MB to allow this HashAggregate to run
            in memory:
              SET work_mem = '77MB';
            For a permanent change, set it per role or in postgresql.conf:
              ALTER ROLE <role> SET work_mem = '77MB';
            Note: work_mem is a per-operation budget. A query with multiple sorts
            or hash joins can use it several times simultaneously.
```

---

## What to do when this fires

**1. Identify the spilling node and its working set size.**

The finding includes the approximate MB. This is your minimum `work_mem` target for this one operation.

```sql
EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) <your query>;
-- Look for the node with high Temp Written Blocks / Temp Read Blocks
```

**2. Increase `work_mem` to eliminate the spill.**

```sql
-- Test in the current session only
SET work_mem = '77MB';
EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) <your query>;
-- Confirm Temp Written Blocks and Temp Read Blocks drop to 0
```

**3. Apply permanently with care.**

`work_mem` is a **per-operation** budget, not a per-connection or per-query budget. A single query with two `HashAggregate` nodes and a sort consumes up to `3 × work_mem` simultaneously. Setting it globally too high causes out-of-memory failures under concurrent load.

```sql
-- Per role (safest for analytical workloads)
ALTER ROLE analyst SET work_mem = '128MB';

-- Per database
ALTER DATABASE mydb SET work_mem = '32MB';

-- Globally in postgresql.conf
work_mem = '16MB'
```

**4. Reduce the working set instead of raising memory.**

Sometimes raising `work_mem` is not practical. Alternatives:

- **For GROUP BY / DISTINCT:** add a partial index or pre-filter rows to reduce distinct key cardinality before the aggregate.
- **For window functions:** partition your query into smaller batches, or use a materialized view pre-aggregated at a coarser granularity.
- **For materialized CTEs (PostgreSQL ≤ 11):** rewrite as a subquery if the CTE is only referenced once — PostgreSQL 12+ un-materializes single-reference CTEs automatically.
- **Run ANALYZE:** if stale statistics caused a bad plan choice (e.g., the planner chose HashAggregate expecting few groups but got millions), fresh statistics may cause the planner to choose a different strategy.

**5. Check whether the spill is coming from a Sort or Hash child.**

If the node with high temp blocks is a `Sort` or `Hash` node, `SortSpill` or `HashJoinSpill` will fire instead (those rules are excluded from `HighTempBlockIO`). Run all three rules together for complete coverage:

```go
adv := advisor.New(
    rules.SortSpill(),
    rules.HashJoinSpill(),
    rules.HighTempBlockIO(),
)
```

---

## Known limitations and false positives

**`BUFFERS` is required.** If EXPLAIN is run without `BUFFERS`, the rule is always silent — even for heavy spills. Findings from this rule are only possible when the input JSON was produced by `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)`.

**Threshold is based on `Plan Rows` (estimated), not actual bytes.** The block count comes from actual execution (it is an ANALYZE field), but the threshold is compared against the raw block count rather than accounting for block size variations. Non-standard `block_size` compilations (rare) would make the MB estimate wrong.

**`work_mem` suggestion is a lower bound.** The suggestion computes `ceil(temp_blocks × 8kB / 1MB)`. This is the amount of temp data spilled — but `work_mem` must be large enough to hold the *entire* working set, not just the overflow. The true target may be 2–5× the spill size. Use the suggestion as a starting point and iterate.

**Concurrent queries share the disk.** A single query's temp I/O is visible in isolation via EXPLAIN. In production with many concurrent queries, the total temp I/O competes for disk bandwidth and the impact is higher than what a single EXPLAIN run shows.

---

## Related rules

- **[SortSpill](sort_spill.md)** — covers Sort nodes specifically. Sort nodes are excluded from `HighTempBlockIO` to avoid duplicate findings. If a Sort node appears under a Merge Join or ORDER BY, `SortSpill` fires; if a `HashAggregate` or other non-Sort/Hash node spills, `HighTempBlockIO` fires. Register both rules together for full coverage.
- **[HashJoinSpill](hash_join_spill.md)** — covers Hash nodes specifically via `Hash Batches > 1`. Like SortSpill, Hash nodes are excluded from `HighTempBlockIO`. Both rules share the same root cause (`work_mem` too small) and the same fix.
- **[MergeJoinUnsortedInputs](merge_join_unsorted_inputs.md)** — when a Merge Join has explicit Sort children that spill, both `SortSpill` and `HighTempBlockIO` may fire for Sort nodes (SortSpill fires; HighTempBlockIO is silent on Sort). But if there is also a `HashAggregate` elsewhere in the plan, `HighTempBlockIO` catches it.
- **[RowEstimateMismatch](row_estimate_mismatch.md)** — if the planner underestimated the number of distinct groups in a GROUP BY, it may have allocated too little memory from the start. A `RowEstimateMismatch` on the `HashAggregate` node paired with `HighTempBlockIO` on the same node points to stale statistics as the root cause.
