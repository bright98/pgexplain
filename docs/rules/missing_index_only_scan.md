# Rule: MissingIndexOnlyScan

**Package:** `rules`  
**Constructor:** `rules.MissingIndexOnlyScan(...IndexOnlyScanOption)`  
**Severity:** `Warn`

---

## What is an Index Only Scan?

An **Index Only Scan** is PostgreSQL's most efficient read path for queries that can be answered entirely from an index — without touching the main table (the heap) at all.

For an Index Only Scan to work, two conditions must hold:

**1. The index must cover all required columns.**  
Every column referenced in the `SELECT` list, `WHERE` clause, and `ORDER BY` must exist in the index. This is called a *covering index*. If any column is missing from the index, PostgreSQL must visit the heap to fetch it — and the node becomes a plain Index Scan instead.

**2. The visibility map must mark the page as all-visible.**  
PostgreSQL's MVCC model means that not every row visible to one transaction is visible to all transactions. When a heap page is all-visible — meaning every row on that page is visible to all current transactions — PostgreSQL can skip the heap check entirely and serve the row from the index alone.

The visibility map is a compact bitmap that tracks which heap pages are all-visible. `VACUUM` maintains it: when a page is vacuumed and found to be all-visible, it is marked in the map. Future Index Only Scans can consult the map and skip the heap for those pages.

When the visibility map is not up to date — because `VACUUM` has not run recently — PostgreSQL must fall back to a heap visit for each row to confirm visibility. These extra heap visits are counted in the **`Heap Fetches`** field of EXPLAIN output.

```
-> Index Only Scan using users_email_idx on users  (cost=0.42..312.44 rows=500 width=36)
                                                    (actual time=0.031..4.812 rows=500 loops=1)
     Index Cond: (email = 'x@example.com')
     Heap Fetches: 200
```

- `rows=500` — 500 rows returned
- `Heap Fetches: 200` — 200 of those rows required a heap visit (40%)
- Ideal value is **0** — every row served from the index alone

---

## Why does it matter?

When `Heap Fetches` is high, the Index Only Scan degrades toward a plain Index Scan. In the worst case (every row hits the heap), it is actually *slower* than a plain Index Scan because it still does the index traversal but then also visits the heap for each row.

The performance gap is most visible on large tables with cold caches or tables that receive frequent writes. On a freshly vacuumed, read-heavy table, `Heap Fetches` should be 0 or very close to it.

---

## What does this rule check?

The rule examines three fields:

| Field | Meaning |
|---|---|
| `Heap Fetches` | Rows that required a heap visit for MVCC visibility check |
| `Actual Rows` | Rows produced per loop execution (ANALYZE required) |
| `Actual Loops` | How many times this node executed |

**The check:**

```
true_actual_rows = Actual Rows × Actual Loops
ratio = Heap Fetches / true_actual_rows

if ratio >= minHeapFetchRatio   (default: 0.1)
  → emit Warn finding
```

A ratio of `0.0` means the scan was fully index-covered — ideal. A ratio of `1.0` means every row required a heap visit — equivalent to a plain Index Scan with extra overhead.

**ANALYZE required.** If EXPLAIN was run without ANALYZE, `Actual Rows` and `Actual Loops` are absent and the rule stays silent.

**Zero handling.** If both `Actual Rows × Actual Loops` and `Heap Fetches` are 0 (empty result, nothing fetched), the rule stays silent — there is nothing meaningful to report.

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
    rules.MissingIndexOnlyScan(),
)

for _, f := range adv.Analyze(plan) {
    node, _ := plan.NodeByID(f.NodeID)
    fmt.Printf("[%s] %s\n", f.Severity, f.Message)
    fmt.Printf("  node:       %s on %q\n", node.NodeType, *node.RelationName)
    fmt.Printf("  heap fetches: %d\n", *node.HeapFetches)
    fmt.Printf("  detail:     %s\n", f.Detail)
    fmt.Printf("  suggestion: %s\n\n", f.Suggestion)
}
```

---

## Options

### `WithMinHeapFetchRatio(ratio float64)`

The minimum fraction of rows that must hit the heap before a finding is emitted.

```
ratio = Heap Fetches / (Actual Rows × Actual Loops)
```

Default: **0.1** (10%).

```go
rules.MissingIndexOnlyScan()                                   // warn at 10%+
rules.MissingIndexOnlyScan(rules.WithMinHeapFetchRatio(0.5))   // only flag severe degradation
rules.MissingIndexOnlyScan(rules.WithMinHeapFetchRatio(0.01))  // catch even minor heap fetches
```

**Choosing a threshold:**
- `0.1` (10%) is a sensible default. A freshly vacuumed table should have near-zero heap fetches. 10% means something meaningful is going wrong.
- `0.5` or higher is appropriate if you only want to flag scans where the Index Only Scan is significantly degraded — more than half the benefit is gone.
- Values near `0` will catch any heap fetch at all, which can be noisy on tables with moderate write rates.

---

## Sample finding

For an Index Only Scan returning 500 rows with 200 heap fetches (40%):

```
Severity:   WARN
NodeID:     1
NodeType:   Index Only Scan

Message:    Index Only Scan on "users" (index: users_email_idx) fetched 40% of rows from the heap

Detail:     An Index Only Scan should serve rows entirely from the index without touching
            the heap. This node returned 500 rows but fetched 200 from the heap (40%).
            Heap fetches happen when the visibility map does not mark the page as all-visible,
            forcing PostgreSQL to verify row visibility in "users". This typically means VACUUM
            has not run recently enough on the table.

Suggestion: Run VACUUM on users to update the visibility map and allow future Index Only Scans
            to skip heap fetches:
              VACUUM users;
            For tables with frequent writes, consider increasing autovacuum frequency:
              ALTER TABLE users SET (autovacuum_vacuum_scale_factor = 0.01);
```

---

## What to do when this fires

**1. Run VACUUM.**

The most common cause is that `VACUUM` has not run recently enough to mark heap pages as all-visible. Run it manually to fix the immediate problem:

```sql
VACUUM users;
```

Then re-run EXPLAIN and check if `Heap Fetches` dropped significantly. On a read-heavy table with stable data, it should drop to 0 or very close.

**2. Tune autovacuum for write-heavy tables.**

If the table receives frequent `INSERT`, `UPDATE`, or `DELETE` activity, the visibility map can go stale quickly. Make autovacuum run more aggressively:

```sql
ALTER TABLE users SET (
    autovacuum_vacuum_scale_factor = 0.01,  -- vacuum when 1% of rows change (default: 20%)
    autovacuum_vacuum_cost_delay = 2        -- reduce throttling (default: 2ms, increase for less I/O impact)
);
```

**3. Verify the index is actually covering.**

If `Heap Fetches` is high even on a freshly vacuumed table, the index may not be a true covering index. Check that every column needed by the query is in the index:

```sql
-- Check the index definition
\d users_email_idx

-- Or query pg_index directly
SELECT indexdef FROM pg_indexes WHERE indexname = 'users_email_idx';
```

If columns are missing, rebuild the index with `INCLUDE` to add the missing columns without making them part of the sort key:

```sql
CREATE INDEX users_email_covering_idx ON users (email) INCLUDE (name, created_at);
```

**4. Check for HOT updates.**

`UPDATE` statements that modify non-indexed columns use Heap Only Tuples (HOT), which do not invalidate the index entry. These do not directly invalidate the visibility map. However, very high update rates on any column can leave pages non-all-visible. If the table has high update throughput, the autovacuum tuning in step 2 is the right lever.

---

## Known limitations and false positives

**New or recently loaded tables.** After a bulk `INSERT` or `COPY`, the visibility map is empty — every page must be vacuumed before it is marked all-visible. An Index Only Scan immediately after a bulk load will show high `Heap Fetches` even though VACUUM will fix it. Consider running `VACUUM ANALYZE <table>` after large data loads.

**Wraparound vacuums.** PostgreSQL periodically forces a vacuum to prevent transaction ID wraparound. These vacuums also update the visibility map, so they may briefly make `Heap Fetches` jump on tables that were previously clean.

**Low-cardinality results.** If the query returns very few rows but `Heap Fetches` equals the row count, the ratio is 100% — but the absolute cost is tiny. The rule does not apply a `minRows` floor by default; use a higher `minHeapFetchRatio` threshold if you want to suppress findings on small result sets.

---

## Related rules

- **[SeqScan](seq_scan.md)** — if statistics are stale (a common cause of high heap fetches on non-all-visible pages), the planner may also choose a Seq Scan instead of the Index Only Scan. Run `ANALYZE` after `VACUUM` to refresh both.
- **[RowEstimateMismatch](row_estimate_mismatch.md)** — stale statistics often accompany a degraded visibility map. If this rule fires alongside RowEstimateMismatch, running `VACUUM ANALYZE` addresses both.
