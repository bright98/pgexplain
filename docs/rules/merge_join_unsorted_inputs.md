# Rule: MergeJoinUnsortedInputs

**Package:** `rules`  
**Constructor:** `rules.MergeJoinUnsortedInputs()`  
**Severity:** `Warn`

---

## What is a Merge Join?

A **Merge Join** is one of three join algorithms PostgreSQL can choose (alongside Nested Loop and Hash Join). It works by reading two input streams in parallel, advancing each stream in sorted order on the join key, and emitting pairs whose keys match — like merging two sorted lists.

The critical requirement: **both inputs must arrive in sorted order on the join key**. The merge step itself is cheap (O(N + M)). The problem arises when the inputs are *not* already sorted.

When a sorted source exists — an Index Scan on the join column, for example — PostgreSQL can use it directly and the merge costs almost nothing. When no sorted source exists, PostgreSQL inserts explicit **Sort nodes** as direct children of the Merge Join to sort each side before the merge begins:

```
Merge Join  (cost=28500..41200 rows=48000)
  Merge Cond: (o.customer_id = c.id)
  ->  Sort  (cost=18200..18320 rows=48000)          ← explicit sort added
        Sort Key: o.customer_id
        Sort Method: external merge  Disk: 14336kB
        ->  Seq Scan on orders o  ...
  ->  Sort  (cost=10300..10362 rows=25000)          ← explicit sort added
        Sort Key: c.id
        Sort Method: quicksort  Memory: 892kB
        ->  Seq Scan on customers c  ...
```

These Sort nodes are the signal this rule detects.

---

## Why does it matter?

Each explicit Sort node adds **O(N log N) CPU work**:

- For 48,000 rows, that is approximately 48,000 × 16 ≈ 768,000 comparison operations just for the outer side.
- If the data exceeds `work_mem`, the sort spills to disk (external merge sort), adding sequential I/O on top of the CPU cost — every row is written to a temp file and read back at least once.

The combined cost of sorting both sides can easily dwarf the merge join itself.

More importantly: the presence of explicit Sort nodes almost always signals **missing indexes on the join key columns**. The same indexes that would eliminate the Sort nodes would also speed up individual lookups on those columns in other queries.

---

## The critical detail

The Sort nodes visible in EXPLAIN are separate from any `ORDER BY` in the query. They exist solely to satisfy the Merge Join's ordering requirement. If the query also has an `ORDER BY` on the join key, the planner *may* share one Sort between the join and the output ordering — but the explicit Sort nodes visible as children of the Merge Join are always added for the join, not the query's final ordering.

---

## Common causes

| Cause | Why it produces explicit Sort nodes |
|---|---|
| No index on either join column | PostgreSQL has no sorted access path; must sort from a Seq Scan |
| Index exists but is not usable | Wrong column order, expression on join key (`WHERE a.x + 1 = b.x`), or type mismatch |
| Tables were just loaded or heavily modified | Planner statistics are stale; planner chose Merge Join over Hash Join incorrectly |
| Multi-column join with no composite index | Index on single column is insufficient for a multi-column join key |
| Index exists but planner chose not to use it | Table is small enough that a Seq Scan + Sort is cheaper than an Index Scan — this is correct planner behavior for tiny tables |

---

## What does this rule check?

```
IF node is a Merge Join node
AND at least one direct child has Node Type == "Sort"
   with Parent Relationship == "Outer" or "Inner"
AND max(Sort child PlanRows) >= minSortRows  (default: 0, always fires)
→ emit Warn finding
```

**Fields read:**

| Node | Field | Meaning |
|---|---|---|
| Merge Join | `Plans` | Direct children examined for Sort nodes |
| Sort child | `Node Type` | Must be `"Sort"` |
| Sort child | `Parent Relationship` | `"Outer"` or `"Inner"` |
| Sort child | `Plan Rows` | Used for threshold check |
| Sort child | `Sort Key` | Reported in finding message and suggestion |
| Sort child | `Sort Space Type` | Optional: whether the sort spilled to disk |
| Sort child | `Sort Space Used` | Optional: kB used, included in detail if present |

**ANALYZE requirement:** None. The Sort nodes are visible in any `EXPLAIN` output, with or without `ANALYZE`.

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
    rules.MergeJoinUnsortedInputs(),
)

for _, f := range adv.Analyze(plan) {
    fmt.Printf("[%s] %s\n", f.Severity, f.Message)
    fmt.Printf("  detail:     %s\n", f.Detail)
    fmt.Printf("  suggestion: %s\n\n", f.Suggestion)
}
```

---

## Options

### `WithMinMergeJoinSortRows(n int64)`

**Default:** `0` (always fire, regardless of row count)

Sets the minimum row count (from the largest Sort child's `Plan Rows`) required to emit a finding. When this option is non-zero, the rule stays silent if all Sort children have fewer than `n` estimated rows.

**Usage variants:**

```go
// Always fire (default — catches even trivial sorts for audit purposes)
rules.MergeJoinUnsortedInputs()

// Only flag when the larger Sort side exceeds 1000 rows
rules.MergeJoinUnsortedInputs(rules.WithMinMergeJoinSortRows(1000))

// Useful in CI: flag anything above a small threshold, not just huge tables
rules.MergeJoinUnsortedInputs(rules.WithMinMergeJoinSortRows(500))
```

**Guidance on choosing a value:**

- `0` (default) is best for auditing all joins in a schema — a Sort on 50 rows is harmless, but finding it early prevents the same pattern from scaling up.
- `1000` is a pragmatic threshold for query performance work — sorts under 1000 rows contribute negligible runtime even without an index.
- Use the planner's `Plan Rows` on the Sort child as a rough guide: if the table typically has N rows, set the threshold to something meaningful for your workload.

---

## Sample findings

For the canonical case where both inputs are sorted and the outer side spills to disk:

```
Severity:   WARN
NodeID:     1
NodeType:   Merge Join

Message:    merge join sorted both inputs: outer on (o.customer_id), inner on (c.id)

Detail:     Merge Join requires both inputs to arrive pre-sorted on the join key.
            Without an index scan that produces rows in sorted order, PostgreSQL inserts
            explicit Sort nodes for each unsorted input, adding O(N log N) work before
            the join can begin. Outer side: 48000 estimated rows sorted on (o.customer_id)
            (14336 kB, spilled to disk). Inner side: 25000 estimated rows sorted on (c.id).

Suggestion: Create indexes on the join key columns to provide pre-sorted inputs,
            eliminating the Sort overhead:
              CREATE INDEX ON <outer_table> (customer_id);
              CREATE INDEX ON <inner_table> (id);
            After indexing, the planner may choose Index Scans that satisfy the merge
            order without sorting. Re-run EXPLAIN (ANALYZE, FORMAT JSON) to confirm the
            Sort nodes disappear.
```

When only one side is sorted (the other already uses an Index Scan):

```
Message:    merge join sorted outer input on (o.customer_id)
```

---

## What to do when this fires

**1. Check the Sort Key and identify the join column.**

The Sort Key in the EXPLAIN output tells you exactly which column(s) need an index. The alias prefix (`o.customer_id`) tells you the table alias; cross-reference with the query to find the actual table name.

**2. Create an index on the join key column.**

```sql
-- Eliminate the outer Sort node
CREATE INDEX ON orders (customer_id);

-- Eliminate the inner Sort node
CREATE INDEX ON customers (id);
```

After creating the index(es), re-run `EXPLAIN (ANALYZE, FORMAT JSON)` and confirm the Sort nodes are replaced by Index Scans.

**3. Re-run EXPLAIN to verify the plan changed.**

```sql
EXPLAIN (ANALYZE, FORMAT JSON)
SELECT o.id, o.total, c.name
FROM orders o JOIN customers c ON o.customer_id = c.id;
```

Look for the Sort nodes to disappear and be replaced by Index Scans. The Merge Join itself may remain, or the planner may switch to a different join strategy (Hash Join) — both are improvements over the original plan.

**4. Run ANALYZE if statistics are stale.**

If the planner chose Merge Join with explicit sorts over Hash Join despite a large hash table, stale statistics may be the root cause. Fresh statistics can cause the planner to reconsider the join strategy.

```sql
ANALYZE orders;
ANALYZE customers;
```

**5. Evaluate whether Merge Join is the right strategy at all.**

After adding indexes, the planner may still choose Merge Join (now using Index Scans for sorted input). This is valid and efficient. If performance is still poor, compare with `SET enable_mergejoin = off` to force the planner to try Hash Join or Nested Loop instead.

---

## Known limitations and false positives

**Trivial row counts.** Sorting 50 rows adds microseconds of overhead. With the default `minSortRows = 0`, the rule fires even on tiny tables. Use `WithMinMergeJoinSortRows(1000)` to focus on cases that matter.

**Query already requires sorting.** If the query has `ORDER BY o.customer_id` and the planner uses a Merge Join on that column, the Sort node may be shared between the join's ordering requirement and the final `ORDER BY`. In this case, the sort would happen anyway — the finding is technically accurate (the Sort node exists) but the fix (adding an index) may or may not eliminate it depending on the query. EXPLAIN output won't distinguish these cases.

**Index won't always be used.** After adding an index, the planner may still use a Seq Scan + Sort if the table is small enough that the index would be slower. This is correct behavior — the finding is informational in that case.

**`Plan Rows` is an estimate.** The `minSortRows` threshold is compared against the planner's row estimate, which may be wrong. A Sort over an underestimated 500 rows might actually sort 500,000 rows at runtime. Run with `ANALYZE` and check `Actual Rows` × `Actual Loops` on the Sort node to confirm the real cost.

---

## Related rules

- **[SortSpill](sort_spill.md)** — fires on the Sort child nodes when they exceed `work_mem` and write to disk. A Merge Join with unsorted inputs that also spills triggers both rules simultaneously. The fix for `MergeJoinUnsortedInputs` (adding an index) eliminates the Sort nodes entirely and thus resolves the spill as well — no `work_mem` tuning needed.
- **[HashJoinSpill](hash_join_spill.md)** — the alternative join strategy. If a Hash Join is chosen instead of Merge Join, this rule fires when the hash table spills. After adding indexes to a Merge Join plan, the planner may switch to Hash Join — check whether it spills.
- **[NestedLoopLarge](nested_loop_large.md)** — another join strategy that degrades when the inner side is not indexed. Missing indexes on join columns affect all three join strategies; check related findings together.
- **[RowEstimateMismatch](row_estimate_mismatch.md)** — if the planner underestimated the rows on a Sort child, the Merge Join strategy may have been chosen incorrectly. Stale statistics cause both wrong plan choices and wrong sort size estimates.
