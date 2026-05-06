# Rule: HashJoinSpill

**Package:** `rules`  
**Constructor:** `rules.HashJoinSpill()`  
**Severity:** `Warn`

---

## What is a Hash Join?

When PostgreSQL joins two tables, it picks one of three strategies based on the estimated size of the relations and whether they are already sorted. **Hash Join** is the strategy used for large, unsorted datasets.

A Hash Join has two phases:

**Build phase** — scan the inner (smaller) relation and load it into a **hash table in memory**. Each row is hashed on the join key. This is the expensive upfront cost.

**Probe phase** — scan the outer (larger) relation row by row. For each row, hash its join key and look it up in the hash table. Matches are emitted immediately.

When the hash table fits entirely in memory, this is extremely efficient — each relation is read exactly once.

---

## What is a spill?

The hash table lives in memory allocated from **`work_mem`** — a per-operation memory budget (default: **4MB**). If the inner relation's hash table fits within `work_mem`, everything stays in memory.

If it does not fit, PostgreSQL falls back to a **batched** approach:

1. Both relations are divided into **N batches** based on a hash of the join key
2. Each batch is written to **temporary disk files**
3. PostgreSQL processes one batch at a time: reads it from disk into memory, performs the join, and moves on

This is indicated by **`Hash Batches > 1`** in the EXPLAIN output.

The cost is severe: data that should be read once is written to disk and read back N times. A join with 8 batches performs roughly 8× the I/O of an in-memory join, plus the overhead of writing and reading temp files.

---

## Where to look in the plan

The spill information lives on the **Hash node** — the inner child of a Hash Join that builds the hash table. Not on the Hash Join node itself.

```
-> Hash Join  (cost=31.50..3025.10 rows=1509 width=104)
              (actual time=2.45..38.23 rows=1842 loops=1)
     Hash Cond: (o.customer_id = c.id)
     -> Seq Scan on orders ...          ← outer (probe side)
     -> Hash                            ← inner (build side) ← rule fires here
          Buckets: 1024  Batches: 4  Memory Usage: 4096kB
          -> Seq Scan on customers ...
```

| Field on Hash node | Meaning |
|---|---|
| `Hash Batches` | **1 = in memory (good). > 1 = spilled to disk (bad)** |
| `Peak Memory Usage` | Memory used for the largest single batch, in kB |
| `Hash Buckets` | Number of hash buckets allocated (affects collision rate, not spill) |

> **Note:** PostgreSQL also reports `Original Hash Batches` in some versions — the number of batches planned before execution started. If this differs from `Hash Batches`, the spill was unexpected mid-execution (the planner thought it would fit). This field is not yet captured in the parser and will be added in a future release.

---

## What does this rule check?

```
IF node is a Hash node
AND Hash Batches > 1
→ emit Warn finding
```

There is no configurable threshold — **any spill is worth flagging**. Even a 2-batch spill means the hash table overflowed `work_mem` and wrote data to disk. The finding includes `Peak Memory Usage` to give you a concrete `work_mem` target.

The rule is silent when:
- The node type is not `Hash`
- `Hash Batches` is absent (EXPLAIN run without ANALYZE on an older PostgreSQL version)
- `Hash Batches` equals 1 (entirely in memory — this is the happy path)

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
    rules.HashJoinSpill(),
)

for _, f := range adv.Analyze(plan) {
    node, _ := plan.NodeByID(f.NodeID)
    fmt.Printf("[%s] %s\n", f.Severity, f.Message)
    fmt.Printf("  batches:    %d\n", *node.HashBatches)
    fmt.Printf("  peak mem:   %dkB\n", *node.PeakMemoryUsage)
    fmt.Printf("  suggestion: %s\n", f.Suggestion)
}
```

---

## Sample finding

For a Hash Join where the inner relation used 4096kB per batch across 4 batches:

```
Severity:   WARN
NodeID:     3
NodeType:   Hash

Message:    hash join spilled to disk across 4 batches (peak memory: 4096kB per batch)

Detail:     The hash table for this join exceeded work_mem and was split into 4 batches.
            Each batch was written to and read from temporary disk files.
            The join processed approximately 16384kB of hash table data across all batches.
            Disk-based batching is orders of magnitude slower than an in-memory hash join.

Suggestion: Increase work_mem to at least 16MB to allow this join to execute entirely
            in memory:
              SET work_mem = '16MB';
            For a permanent change, set work_mem = '16MB' in postgresql.conf or per role:
              ALTER ROLE <role> SET work_mem = '16MB';
```

---

## What to do when this fires

**1. Estimate the required `work_mem`.**

The finding computes this for you: `Hash Batches × Peak Memory Usage`. For example, 4 batches at 4096kB each = 16384kB = **16MB**. Setting `work_mem` to at least this value should reduce the join to a single batch.

```sql
-- For the current session only (safe to test without affecting other queries)
SET work_mem = '16MB';
EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) <your query>;
```

**2. Verify the spill is gone.**

After raising `work_mem`, re-run EXPLAIN and confirm `Hash Batches` dropped to 1.

**3. Apply permanently with care.**

`work_mem` is a **per-operation** budget, not a per-connection budget. A single complex query with multiple sorts and hash joins can use `work_mem` several times over simultaneously. Setting it globally to a large value can cause memory exhaustion under concurrent load.

Options from least to most aggressive:

```sql
-- Per role (only affects that role's sessions)
ALTER ROLE analytics_user SET work_mem = '64MB';

-- Per database
ALTER DATABASE mydb SET work_mem = '32MB';

-- Globally in postgresql.conf
work_mem = '16MB'
```

A common production pattern: keep the global `work_mem` conservative (8–16MB) and raise it per session only for known heavy analytical queries.

**4. Check if the inner relation can be reduced.**

Sometimes the spill is caused by an unnecessarily large inner side. Consider whether you can:
- Add a `WHERE` clause to reduce the inner relation before the join
- Use a subquery to pre-filter
- Change the join order so the smaller relation is on the inner side (though the planner usually handles this)

---

## Known limitations and false positives

**`work_mem` is per operation, not per query.** A query with two hash joins uses up to `2 × work_mem`. The suggestion in this rule assumes the entire `work_mem` budget is available for this one join, which may not be true under concurrent load.

**Peak Memory Usage reflects one batch.** `Peak Memory Usage` is the maximum memory used for a single batch, not the total data size. The formula `batches × peak` is an approximation — the real total depends on data distribution across batches. Use the suggestion as a starting point and verify with EXPLAIN.

**Intentional batching.** For extremely large joins, even a large `work_mem` may not help — the data is simply too large to fit in memory. In those cases, consider whether the join can be broken up, pre-aggregated, or offloaded to a batch pipeline.

---

## Related rules

- **[RowEstimateMismatch](row_estimate_mismatch.md)** — if the planner underestimated the inner relation's row count, it may have allocated too few hash buckets and too little memory from the start, making a spill more likely. The two findings often appear together.
- **[NestedLoopLarge](nested_loop_large.md)** — if the planner chose a Hash Join because it estimated a large inner set, but the estimate was wrong, a Nested Loop might have been a better choice. Or vice versa.
- **[SortSpill](sort_spill.md)** — Sort spills have the same root cause as Hash Join spills: `work_mem` too small. If both fire on the same query, the required `work_mem` is the sum of the two, not the max.
- **[ParallelNotLaunched](parallel_not_launched.md)** — if a parallel hash join spills, check whether the intended workers launched. Fewer workers means each worker's hash table is larger and more likely to exceed `work_mem`.
