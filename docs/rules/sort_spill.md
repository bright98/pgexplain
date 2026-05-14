# Rule: SortSpill

**Package:** `rules`  
**Constructor:** `rules.SortSpill()`  
**Severity:** `Warn`

---

## What is a Sort in PostgreSQL?

When a query contains `ORDER BY`, `GROUP BY`, a window function (`OVER`), or certain join strategies, PostgreSQL needs to physically order a set of rows. This is done by a **Sort** node in the plan tree.

The sort runs in memory allocated from **`work_mem`** — a per-operation memory budget (default: **4MB**). PostgreSQL picks a sort strategy based on whether the data fits in that budget:

| Sort Method | Where | When |
|---|---|---|
| **quicksort** | Memory | All rows fit in `work_mem` |
| **top-N heapsort** | Memory | `ORDER BY ... LIMIT N` and N is small — only keeps the top N rows |
| **external merge** | **Disk** | Rows exceed `work_mem` — data is written to temp files |

You can see which strategy was chosen in EXPLAIN output:

```
-> Sort  (cost=15420.44..17920.44 rows=100000 width=72)
         (actual time=892.3..1203.9 rows=100000 loops=1)
     Sort Key: created_at DESC
     Sort Method: external merge  Disk: 18432kB
```

Or for an in-memory sort:

```
     Sort Method: quicksort  Memory: 4096kB
```

---

## What is a spill?

When the input exceeds `work_mem`, PostgreSQL falls back to **external merge sort**:

1. Read as many rows as fit in `work_mem`, sort them in memory, write the sorted chunk to a **temporary disk file**
2. Repeat for the next chunk
3. Once all chunks are written, **merge** the sorted temp files into a single sorted output

The word "external" is the classical computer science term for sorting data that does not fit in main memory. It is always disk-based — there is no in-memory `external merge`.

The cost is significant:
- Every row is **written to disk** during the chunk phase
- Every row is **read back from disk** during the merge phase
- With many chunks, multiple merge passes may be needed, multiplying the I/O further

An in-memory quicksort touches each row exactly once. An external merge sort touches it at least twice, and more with large data sets.

---

## Where to look in the plan

The spill information lives directly on the **Sort node**:

| Field | Meaning |
|---|---|
| `Sort Method` | `"quicksort"`, `"top-N heapsort"`, or `"external merge"` |
| `Sort Space Type` | `"Memory"` or `"Disk"` |
| `Sort Space Used` | kB of memory or disk used |

`Sort Space Type: "Disk"` and `Sort Method: "external merge"` always appear together. Either field alone is a reliable signal.

---

## What does this rule check?

```
IF node is a Sort node
AND Sort Space Type == "Disk"
→ emit Warn finding
```

There is no configurable threshold — **any disk spill is worth flagging**. An in-memory sort is always faster than an external merge sort. The finding includes `Sort Space Used` to give you a concrete `work_mem` target.

The rule is silent when:
- The node type is not `Sort`
- `Sort Space Type` is absent (EXPLAIN run without ANALYZE on an older PostgreSQL version)
- `Sort Space Type` is `"Memory"` — the sort ran in memory, which is the happy path

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
    rules.SortSpill(),
)

for _, f := range adv.Analyze(plan) {
    node, _ := plan.NodeByID(f.NodeID)
    fmt.Printf("[%s] %s\n", f.Severity, f.Message)
    fmt.Printf("  sort method: %s\n", *node.SortMethod)
    fmt.Printf("  disk used:   %d kB\n", *node.SortSpaceUsed)
    fmt.Printf("  suggestion:  %s\n\n", f.Suggestion)
}
```

---

## Sample finding

For a Sort node that used 18432kB of disk:

```
Severity:   WARN
NodeID:     1
NodeType:   Sort

Message:    sort spilled to disk using external merge (18432 kB)

Detail:     The sort could not fit in work_mem and wrote temporary data to disk.
            PostgreSQL sorted chunks in memory, wrote them to temp files, then merged
            the files — reading and writing the sorted data at least twice.
            Disk usage for this sort: 18432 kB.
            An in-memory sort (quicksort) is significantly faster because it avoids
            all disk I/O.

Suggestion: Increase work_mem to at least 18MB to allow this sort to run in memory:
              SET work_mem = '18MB';
            For a permanent change, set it per role or in postgresql.conf:
              ALTER ROLE <role> SET work_mem = '18MB';
            Note: work_mem is a per-operation budget. A query with multiple sorts
            or hash joins can use it several times simultaneously.
```

---

## What to do when this fires

**1. Estimate the required `work_mem`.**

The finding computes this for you: `Sort Space Used` rounded up to the nearest MB. For example, 18432kB → 18MB. Setting `work_mem` to at least this value should allow the sort to run in memory.

```sql
-- Test in the current session only
SET work_mem = '18MB';
EXPLAIN (ANALYZE, FORMAT JSON) <your query>;
```

Re-run EXPLAIN and confirm `Sort Space Type` changed to `"Memory"`.

**2. Apply permanently with care.**

`work_mem` is a **per-operation** budget, not a per-connection budget. A single query with multiple Sort and Hash Join nodes can consume `work_mem` several times simultaneously. Setting it globally too high causes memory exhaustion under concurrent load.

```sql
-- Per role (safest for analytical workloads)
ALTER ROLE analytics_user SET work_mem = '64MB';

-- Per database
ALTER DATABASE mydb SET work_mem = '32MB';

-- Globally in postgresql.conf
work_mem = '16MB'
```

A common pattern: keep the global `work_mem` conservative (8–16MB) and raise it per session for known heavy queries.

**3. Check if the sort can be eliminated with an index.**

The cheapest sort is no sort at all. If the query has `ORDER BY column` and there is a B-tree index on that column, PostgreSQL can read rows in order from the index and skip the Sort node entirely.

```sql
-- Check if an index exists on the sort key
\d orders

-- Add one if it doesn't
CREATE INDEX ON orders (created_at DESC);
```

After adding the index, re-run EXPLAIN. If the Sort node disappears and is replaced by an Index Scan, the query now runs in O(rows returned) rather than O(rows sorted).

**4. Consider pre-sorting or partial indexes.**

For queries that always sort by the same column and filter by a second column:

```sql
-- A partial index pre-orders rows for a common filter
CREATE INDEX ON orders (created_at DESC) WHERE status = 'pending';
```

---

## Known limitations and false positives

**`work_mem` is per operation, not per query.** A query with two Sort nodes and a Hash Join uses up to `3 × work_mem`. The suggestion in this rule assumes the full `work_mem` budget is available for this one sort, which may not be true under concurrent load. Use the suggestion as a starting point.

**Extremely large datasets.** For very large inputs, even a generous `work_mem` may not help — the data simply won't fit in memory. In those cases, the right fix is architectural: pre-aggregating in a materialized view, adding an index to eliminate the sort, or splitting the query.

**`Sort Space Used` is approximate.** PostgreSQL reports the disk space used after the sort completes. The actual I/O may be higher if multiple merge passes were needed.

---

## Related rules

- **[MergeJoinUnsortedInputs](merge_join_unsorted_inputs.md)** — when a Sort node is a direct child of a Merge Join, both rules fire together. Adding an index on the join key eliminates the Sort node entirely — a better fix than raising `work_mem`, because it removes the sort rather than just making it cheaper.
- **[HashJoinSpill](hash_join_spill.md)** — Hash Join spills follow the same root cause (work_mem too small) and the same fix (raise work_mem or reduce the data size). If both fire on the same query, the required work_mem is the sum, not the max.
- **[RowEstimateMismatch](row_estimate_mismatch.md)** — if the planner underestimated the number of rows entering the Sort, it may have allocated too little work_mem from the start, making a spill more likely even with a reasonable global setting.
- **[TopNHeapsort](top_n_heapsort.md)** — the other sort-related rule. Fires when top-N heapsort reads the full table to serve a small LIMIT, and an index on the sort key could eliminate the full scan entirely.
- **[ParallelNotLaunched](parallel_not_launched.md)** — if a parallel sort spills, check whether the intended number of workers actually launched. Fewer workers means each worker sorts a larger chunk, making a spill more likely.
