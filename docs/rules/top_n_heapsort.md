# Rule: TopNHeapsort

**Package:** `rules`  
**Constructor:** `rules.TopNHeapsort(...TopNHeapsortOption)`  
**Severity:** `Info`

---

## What is top-N heapsort?

When a query has `ORDER BY ... LIMIT N`, PostgreSQL does not need to sort the entire result set. Instead of sorting all rows and discarding most of them, it can maintain a **fixed-size heap of the N best rows seen so far**. This is the **top-N heapsort** strategy.

How it works:
1. Read the first N rows and build a min-heap (for ascending order) or max-heap (for descending)
2. For each remaining row, compare it to the heap root — if it is better than the worst row in the heap, replace the root and re-heapify
3. After reading all rows, the heap contains exactly the top N

```
-> Sort  (cost=0.00..2891.34 rows=10 width=72)
         (actual time=312.5..312.5 rows=10 loops=1)
     Sort Key: created_at DESC
     Sort Method: top-N heapsort  Memory: 25kB
   -> Seq Scan on orders  (actual rows=100000 loops=1)
```

Top-N heapsort is always **in-memory** — the heap size is bounded by N × row width, not by the full input size. It is the right choice when N is small and no index can provide the rows in sorted order.

---

## Why can it still be a problem?

Top-N heapsort is fast — but it still reads **every row of the input**. In the example above, PostgreSQL read all 100,000 rows of `orders` to return 10.

Compare this to what happens when an index on `created_at DESC` exists:

```
-> Limit  (rows=10)
   -> Index Scan Backward using orders_created_at_idx on orders
        (actual rows=10 loops=1)
```

PostgreSQL walks the index in reverse order and stops after 10 rows. It reads **10 rows** instead of 100,000. That is an O(LIMIT) operation instead of O(table size).

The performance difference is negligible on small tables but grows proportionally with table size. On a 10M-row table with `LIMIT 10`, top-N heapsort reads 10M rows; an index scan reads 10.

---

## Why is this `Info` and not `Warn`?

Three reasons:

**1. top-N heapsort is in-memory and genuinely fast.** There is no disk I/O. On a table that fits in the buffer cache, 100,000 row comparisons may take milliseconds. It is not a confirmed problem — it is a missed optimization opportunity.

**2. We cannot see the schema.** The EXPLAIN JSON contains only the plan, not the list of existing indexes. The index on `created_at` may already exist, and the planner chose not to use it — because statistics were stale, because the table is small enough that the planner prefers the Seq Scan, or because the index key does not exactly match the sort key. `ANALYZE` should always be the first step, not `CREATE INDEX`.

**3. The fix is not always an index.** If the sort key is an expression (`ORDER BY lower(name)`), a plain B-tree index on `name` won't help — a functional index is needed. If the sort key has multiple columns, the index must match their order exactly.

This rule fires as a hint to investigate. Whether to act depends on the query's frequency, the table's size, and whether an index would actually be used.

---

## What does this rule check?

The rule inspects the **Sort node and its child**:

| Field | Where | Meaning |
|---|---|---|
| `Sort Method` | Sort node | Must be `"top-N heapsort"` |
| `Sort Key` | Sort node | Columns being sorted — used in the index suggestion |
| `Actual Rows` | Sort node | Rows returned (the effective LIMIT) |
| Child node type | `Plans[0]` | Must be `Seq Scan` — no index was used |
| `Actual Rows × Actual Loops` | Child | Total rows the Seq Scan read |

**The check:**

```
IF node is Sort
AND Sort Method == "top-N heapsort"
AND child node is a Seq Scan
AND child.ActualRows × child.ActualLoops >= minInputRows   (default: 1000)
→ emit Info finding
```

The child must be a **Seq Scan** specifically. If the child is already an Index Scan, the planner used an index but still chose heapsort — the index key may not match the sort key, or heapsort was genuinely cheaper. That case is much harder to advise on and is not flagged.

**ANALYZE required.** The child's `ActualRows` and `ActualLoops` are only present when EXPLAIN is run with ANALYZE. Without them the rule stays silent.

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
    rules.TopNHeapsort(),
)

for _, f := range adv.Analyze(plan) {
    node, _ := plan.NodeByID(f.NodeID)
    fmt.Printf("[%s] %s\n", f.Severity, f.Message)
    fmt.Printf("  sort key:   %s\n", strings.Join(node.SortKey, ", "))
    fmt.Printf("  detail:     %s\n", f.Detail)
    fmt.Printf("  suggestion: %s\n\n", f.Suggestion)
}
```

---

## Options

### `WithMinInputRows(n int)`

The minimum number of rows the child Seq Scan must read before a finding is emitted. Default: **1000**.

```go
rules.TopNHeapsort()                              // default: 1000+ rows scanned
rules.TopNHeapsort(rules.WithMinInputRows(10000)) // only flag large tables
rules.TopNHeapsort(rules.WithMinInputRows(100))   // catch smaller cases
```

**Choosing a threshold:**
- `1000` avoids noise on small tables where the full scan is cheap.
- Raise it if you only want to flag cases where the table is clearly large enough to justify index maintenance cost.
- Lower it for CI pipelines where you want to catch all cases early, even on small tables.

---

## Sample finding

For `ORDER BY created_at DESC LIMIT 10` over a 100,000-row table:

```
Severity:   INFO
NodeID:     1
NodeType:   Sort

Message:    top-N heapsort on "orders" scanned 100000 rows to return 10

Detail:     The query used top-N heapsort to return 10 rows from "orders".
            This strategy reads every row of the input (100000 rows scanned)
            and keeps only the top N in a fixed-size heap. It is in-memory
            and fast, but still performs a full table scan.
            If a B-tree index exists on (created_at DESC), PostgreSQL could
            use an Index Scan to read rows in sorted order and stop after the
            LIMIT — reducing the scan from 100000 rows to just the rows returned.

Suggestion: First run ANALYZE to ensure statistics are up to date — the planner
            may already have an index but chose this plan due to stale row counts:
              ANALYZE orders;
            If the plan still shows a Seq Scan after ANALYZE, consider adding
            an index on the sort key:
              CREATE INDEX orders_created_at_idx ON orders (created_at DESC);
            After adding the index, re-run EXPLAIN and confirm the Sort node
            is replaced by an Index Scan.
```

---

## What to do when this fires

**1. Run ANALYZE first.**

The planner may already have an appropriate index but chose the Seq Scan because its row count statistics were stale. Fresh statistics can flip the plan without any schema changes:

```sql
ANALYZE orders;
EXPLAIN (ANALYZE, FORMAT JSON) SELECT * FROM orders ORDER BY created_at DESC LIMIT 10;
```

If the Sort node disappears and is replaced by an Index Scan, you are done.

**2. Check if an index on the sort key exists.**

```sql
\d orders
-- or
SELECT indexdef FROM pg_indexes WHERE tablename = 'orders';
```

If an index on `created_at` exists and the planner is still choosing the Seq Scan after ANALYZE, it may have decided the index is not selective enough given the LIMIT. Try forcing it for comparison:

```sql
SET enable_seqscan = off;
EXPLAIN (ANALYZE, FORMAT JSON) SELECT * FROM orders ORDER BY created_at DESC LIMIT 10;
```

This shows what the plan would look like with an Index Scan. Compare the `Actual Total Time` between the two plans to decide if the index actually helps.

**3. Add the index if it does not exist.**

```sql
CREATE INDEX orders_created_at_idx ON orders (created_at DESC);
ANALYZE orders;
EXPLAIN (ANALYZE, FORMAT JSON) SELECT * FROM orders ORDER BY created_at DESC LIMIT 10;
```

Confirm that the Sort node is gone and replaced by an Index Scan or Index Only Scan.

**4. Multi-column sort keys.**

`ORDER BY a, b LIMIT 10` needs an index on `(a, b)` in that exact order. An index on `(a)` alone only helps if `a` has high enough selectivity to make the partial index walk worthwhile.

```sql
CREATE INDEX orders_status_created_idx ON orders (status ASC, created_at DESC);
```

**5. Expression sort keys.**

`ORDER BY lower(name) LIMIT 10` cannot use a plain index on `name`. Create a functional index:

```sql
CREATE INDEX orders_lower_name_idx ON orders (lower(name));
```

---

## Known limitations and false positives

**Index exists but was not used.** The planner may have an index and still prefer the Seq Scan + heapsort if the table is small or if `random_page_cost` is set high (making index traversal look expensive). This finding does not mean an index is missing — it means the planner chose not to use one.

**Expression and multi-column sort keys.** The suggested `CREATE INDEX` uses the sort key string as-is from the plan. For expression keys (`lower(name)`) this produces a valid functional index definition. For multi-column keys the column order in the index must match the sort key order exactly.

**LIMIT not visible in the plan.** PostgreSQL does not emit the LIMIT value in the Sort node's JSON — we infer it from `Sort Method: top-N heapsort` and the Sort node's `Actual Rows`. If the plan has a `Limit` node above the Sort, that node will show the actual limit value.

**Small tables.** On a table with 2,000 rows, a full scan + heapsort may be faster than an index scan due to the overhead of random I/O. Use the `WithMinInputRows` option to tune the threshold for your workload.

---

## Related rules

- **[SortSpill](sort_spill.md)** — the other sort-related rule: fires when a sort cannot fit in `work_mem` and spills to disk. SortSpill is a `Warn`; TopNHeapsort is an `Info`. They fire on different sort strategies and have different fixes.
- **[SeqScan](seq_scan.md)** — if the child Seq Scan also discards many rows via a filter (e.g. `WHERE status = 'active' ORDER BY created_at LIMIT 10`), SeqScan will fire too. A composite index on `(status, created_at)` would address both findings at once.
- **[RowEstimateMismatch](row_estimate_mismatch.md)** — if the planner's row estimate for the child Seq Scan was wildly off, it may have chosen top-N heapsort when it could have used an index. Stale statistics cause both findings; running ANALYZE is the first fix for both.
