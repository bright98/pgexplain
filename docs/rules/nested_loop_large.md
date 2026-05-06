# Rule: NestedLoopLarge

**Package:** `rules`  
**Constructor:** `rules.NestedLoopLarge(...NestedLoopOption)`  
**Severity:** `Error` (inner Seq Scan) · `Warn` (inner Index Scan or other)

---

## What is a Nested Loop join?

PostgreSQL has three join strategies. **Nested Loop** is the simplest: for every row on the outer side, execute the inner side to find matches.

```
for each row in outer:
    execute inner side
    emit matching rows
```

The total work is:

```
outer_rows × cost_per_inner_probe
```

Whether this is fast or catastrophic depends entirely on what the inner side is.

---

## Two very different inner sides

### Inner side has an index (efficient)

When the inner relation has an index on the join key, each probe is O(log n) — a fast B-tree lookup. The planner chooses Nested Loop when it expects a small outer set, because a handful of index lookups is the cheapest possible join strategy.

```
-> Nested Loop
   -> Seq Scan on orders         (outer, 5 rows)      ← small
   -> Index Scan on order_items  (inner, loops=5)     ← 5 fast lookups
```

Five index probes — this is ideal.

### Inner side is a Seq Scan (catastrophic)

When the inner side has no index, each probe reads the **entire inner table** from start to finish. The complexity becomes O(outer × inner) — quadratic.

```
-> Nested Loop
   -> Seq Scan on orders         (outer, 1000 rows)   ← large
   -> Seq Scan on order_items    (inner, loops=1000)  ← 1000 full table scans
```

One thousand full table scans. If `order_items` has 50,000 rows, that is **50,000,000 row reads** for this join.

This is the SQL equivalent of the N+1 query problem — reading one record per outer row rather than joining efficiently.

---

## The signal in EXPLAIN output

The key field is **`Actual Loops` on the inner child node**. It tells you exactly how many times the inner side was executed. This equals the total rows the outer side produced.

```
-> Nested Loop  (actual rows=3000 loops=1)
   -> Seq Scan on orders      actual rows=1000  loops=1    ← outer: 1000 rows
   -> Index Scan on order_items  actual rows=3  loops=1000 ← inner: ran 1000 times
```

- `inner.ActualLoops = 1000` — probed 1000 times
- `inner.ActualRows = 3` — returned 3 rows per probe
- Total inner work: **3000 rows** across 1000 executions

> **Important:** `Actual Rows` is always per loop. The true total is `Actual Rows × Actual Loops`. This rule uses `inner.ActualLoops` as the primary signal — it is the probe count, regardless of how many rows each probe returns.

---

## What does this rule check?

```
IF node is Nested Loop
AND ANALYZE was run (Actual Loops present on inner child)
AND inner child exists (Parent Relationship == "Inner")
AND inner.ActualLoops >= minInnerLoops  (default: 1000)
→ emit finding
```

**Severity depends on the inner node type:**

| Inner side | Severity | Reason |
|---|---|---|
| Seq Scan | **Error** | Full table scan per outer row — O(outer × inner) complexity, almost always a missing index |
| Index Scan, Index Only Scan, or other | **Warn** | Efficient per probe but many probes — may indicate a bad row estimate caused the planner to prefer Nested Loop |

---

## How to use it

```go
import (
    "github.com/Bright98/pgexplain/advisor"
    "github.com/Bright98/pgexplain/parser"
    "github.com/Bright98/pgexplain/rules"
)

plan, _ := parser.Parse(explainJSON)

adv := advisor.New(
    rules.NestedLoopLarge(),
)

for _, f := range adv.Analyze(plan) {
    node, _ := plan.NodeByID(f.NodeID)
    fmt.Printf("[%s] %s\n", f.Severity, f.Message)
    fmt.Printf("  detail:     %s\n", f.Detail)
    fmt.Printf("  suggestion: %s\n\n", f.Suggestion)
}
```

---

## Options

### `WithMinInnerLoops(n int)`

The minimum number of inner-side executions that triggers a finding. Default: **1000**.

```go
rules.NestedLoopLarge()                            // default: 1000+ inner loops
rules.NestedLoopLarge(rules.WithMinInnerLoops(100))  // catch smaller cases
rules.NestedLoopLarge(rules.WithMinInnerLoops(5000)) // only flag extreme cases
```

**Choosing a threshold:**
- `1000` is a reasonable default. A join that executes its inner side 1000 times is doing significant work even with an efficient index lookup.
- Lower values (100–500) are appropriate for latency-sensitive services or when inner nodes are expensive.
- Raising the threshold suppresses findings on queries where many small Nested Loop iterations are genuinely the optimal strategy (e.g. parameterized queries fetching one row at a time by primary key).

---

## Sample findings

**Error — inner Seq Scan (missing index):**

```
Severity:   ERROR
Message:    nested loop performed full table scan on "order_items" 1000 times

Detail:     The inner side of this Nested Loop was executed 1000 times — once per outer row.
            Total rows produced by the inner side: 3000 (1000 loops × 3 rows per loop).
            Because the inner side is a Seq Scan, PostgreSQL reads all rows of "order_items"
            on every iteration — O(outer × inner) complexity.

Suggestion: Add an index on "order_items" to support the join condition (order_id = o.id).
            Without an index on the join key, each of the 1000 outer rows triggers a full
            table scan. After adding the index, re-run EXPLAIN (ANALYZE, FORMAT JSON) to
            confirm the plan changed to an Index Scan.
```

**Warn — inner Index Scan (many probes):**

```
Severity:   WARN
Message:    nested loop probed Index Scan on "order_items" 1000 times

Detail:     The inner side of this Nested Loop was executed 1000 times — once per outer row.
            Total rows produced by the inner side: 3000 (1000 loops × 3 rows per loop).
            Each probe uses an index or other efficient strategy, but the cumulative cost
            of many probes may exceed what a Hash Join or Merge Join would spend.

Suggestion: The planner chose Nested Loop because it estimated a small outer set. With
            1000 actual inner probes, a Hash Join or Merge Join may be more efficient.
            Check whether the outer row estimate is accurate — if not, run ANALYZE on
            the relevant tables to refresh statistics.
```

---

## What to do when this fires

### Error case: inner Seq Scan

This almost always means a **missing index on the join key**. Steps:

1. **Identify the join key** from `Index Cond` or `Join Filter` in the plan.
2. **Add the index:**
   ```sql
   CREATE INDEX ON order_items (order_id);
   ```
3. **Verify** by re-running EXPLAIN — the Seq Scan on the inner side should become an Index Scan, and the planner may now prefer Hash Join or Merge Join entirely.

If an index already exists, check that:
- The index covers the exact column(s) used in the join condition (not a partial or expression index that doesn't apply)
- Statistics are up to date (`ANALYZE order_items`)
- The `enable_nestloop` setting hasn't been manipulated

### Warn case: inner Index Scan

The planner chose Nested Loop because it estimated a small outer set. The probe count may indicate:

1. **Bad row estimate on the outer side** — the planner thought it would have 10 outer rows, got 1000. Check `Plan Rows` vs `Actual Rows` on the outer child. If they diverge significantly, run `ANALYZE` on that table. The [RowEstimateMismatch](row_estimate_mismatch.md) rule will surface this independently.

2. **Plan is actually correct** — if the outer set is truly small at this point in the plan, many index lookups may still be cheaper than building a hash table. Look at `Total Cost` and `Actual Total Time` in context.

3. **Force a different join strategy** (last resort, for testing only):
   ```sql
   SET enable_nestloop = off;
   EXPLAIN (ANALYZE, FORMAT JSON) <your query>;
   ```
   This disables Nested Loop and shows what the planner would choose instead. Never use this in production — fix the root cause (statistics) instead.

---

## The N+1 connection

When you see a Nested Loop with a large `Actual Loops` count, especially with a Seq Scan on the inner side, it is worth checking whether an ORM is generating this plan through an N+1 pattern. The symptom is identical: one outer row triggers one full inner scan. In SQL, the fix is the same — an index on the join key or a rewritten query that joins all rows at once.

---

## Known limitations and false positives

**LIMIT queries.** Nested Loop is often the best strategy when a `LIMIT` is applied, because it can emit the first N matching rows immediately. Hash Join must build the entire hash table before returning any rows. If the query has `LIMIT 10`, a Nested Loop with many planned loops may never execute most of them. Look at `Actual Loops` vs `Plan Rows` — if actual loops is much lower than planned, the LIMIT stopped the loop early.

**Correlated subqueries.** A correlated subquery in the `SELECT` list or `WHERE` clause also shows up as a Nested Loop in the plan. Each outer row re-executes the subquery. The fix is usually to rewrite the subquery as a lateral join or a CTE.

**Very selective inner joins.** If the inner side returns 0 or 1 rows per probe, even 10,000 probes may be fast. Check `Actual Total Time` — if the Nested Loop node is fast overall, the finding is informational rather than urgent.

---

## Related rules

- **[RowEstimateMismatch](row_estimate_mismatch.md)** — if the planner underestimated the outer row count, it may have incorrectly preferred Nested Loop. This rule fires on the join node; RowEstimateMismatch fires on the outer child where the estimate diverged.
- **[HashJoinSpill](hash_join_spill.md)** — if the alternative to Nested Loop is a Hash Join that spills to disk, switching join strategies may not help without also raising `work_mem`.
