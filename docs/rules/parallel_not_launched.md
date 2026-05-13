# Rule: ParallelNotLaunched

**Package:** `rules`  
**Constructor:** `rules.ParallelNotLaunched()`  
**Severity:** `Warn`

---

## What is parallel query execution?

PostgreSQL can split a query across multiple **worker processes** to use more CPU cores simultaneously. Each worker handles a partition of the work — scanning a range of blocks, hashing rows, sorting a chunk — and the results are collected by the **leader process** at a synchronization node.

That synchronization node is either:

- **Gather** — collects rows from workers in arbitrary order (used with Parallel Seq Scan, Parallel Hash Join, etc.)
- **Gather Merge** — collects rows from workers that each produced sorted output, merging them into a final sorted stream (used with Parallel Sort)

```
-> Gather  (cost=1000.0..11234.6 rows=100000 width=72)
           (actual time=12.3..892.2 rows=100000 loops=1)
     Workers Planned: 4
     Workers Launched: 2
   -> Parallel Seq Scan on events  (actual rows=25000 loops=3)
```

Two fields on the Gather node tell you whether parallelism was achieved as planned:

| Field | Meaning |
|---|---|
| `Workers Planned` | How many parallel workers the planner expected to launch |
| `Workers Launched` | How many actually started at runtime |

In the example above, `loops=3` on the child Seq Scan confirms that 3 processes participated (2 workers + 1 leader), not 5 as planned (4 workers + 1 leader).

---

## Why does it matter?

The planner builds the **entire plan** assuming `Workers Planned` workers will run. It divides row estimates, costs, memory allocations, and join strategies across that many processes. When fewer workers launch, those calculations are wrong:

- The query does more work per process than the planner expected
- Sort and hash operations sized for `N` workers run with fewer, potentially spilling
- The overall query takes longer than the planner predicted, silently

This is subtle: the query returns correct results, so the problem is invisible unless you look at the EXPLAIN output and notice the discrepancy.

---

## Common causes

| Cause | Explanation |
|---|---|
| `max_parallel_workers` reached | Global cap on total parallel workers across all queries. Other concurrent queries consumed the budget at the moment this query ran. |
| `max_parallel_workers_per_gather` | Per-Gather limit. The plan asked for more workers than this setting allows. Default is 2, which is often lower than what the planner wants. |
| Worker startup failure | The OS could not fork new worker processes — memory pressure, process limits (`ulimit -u`), or OS-level constraints. |
| `max_worker_processes` reached | Total background worker budget (shared across autovacuum, logical replication, and parallel workers). |

---

## What does this rule check?

```
IF node is Gather or Gather Merge
AND Workers Planned > 0
AND Workers Launched < Workers Planned
→ emit Warn finding
```

There is no configurable threshold — **any gap between planned and launched is worth flagging**. Even one missing worker means the plan is executing under assumptions the planner did not intend.

The rule is silent when:
- The node type is neither `Gather` nor `Gather Merge`
- `Workers Planned` or `Workers Launched` is absent
- `Workers Planned` is 0 (parallelism was disabled for this node)
- `Workers Launched >= Workers Planned` (full parallelism achieved)

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
    rules.ParallelNotLaunched(),
)

for _, f := range adv.Analyze(plan) {
    node, _ := plan.NodeByID(f.NodeID)
    fmt.Printf("[%s] %s\n", f.Severity, f.Message)
    fmt.Printf("  planned:  %d\n", *node.WorkersPlanned)
    fmt.Printf("  launched: %d\n", *node.WorkersLaunched)
    fmt.Printf("  detail:     %s\n", f.Detail)
    fmt.Printf("  suggestion: %s\n\n", f.Suggestion)
}
```

---

## Sample finding

For a Gather node with `Workers Planned: 4`, `Workers Launched: 2`:

```
Severity:   WARN
NodeID:     1
NodeType:   Gather

Message:    Gather launched 2 of 4 planned workers

Detail:     The planner designed this Gather plan for 4 parallel worker(s),
            but only 2 launched at runtime (2 short). Plan costs and row
            estimates assume 4 workers — with fewer workers running, the
            query likely took longer than predicted.

Suggestion: Check whether the parallel worker limits were hit at runtime:
              SHOW max_parallel_workers;            -- global cap across all queries
              SHOW max_parallel_workers_per_gather; -- per-Gather cap
            If either value is below 4, raise it:
              ALTER SYSTEM SET max_parallel_workers = 4;
              ALTER SYSTEM SET max_parallel_workers_per_gather = 4;
              SELECT pg_reload_conf();
            Also check whether other concurrent queries are consuming the worker budget:
              SELECT count(*) FROM pg_stat_activity WHERE backend_type = 'parallel worker';
```

For a Gather with `Workers Launched: 0` (no workers at all):

```
Message:    Gather launched 0 of 4 planned workers

Detail:     ... No workers launched at all — the parallel section ran entirely
            in the leader process.
```

---

## What to do when this fires

**1. Check current limits.**

```sql
SHOW max_parallel_workers;             -- default: 8
SHOW max_parallel_workers_per_gather;  -- default: 2
SHOW max_worker_processes;             -- default: 8 (shared budget)
```

Compare `max_parallel_workers_per_gather` against `Workers Planned`. If it is lower, the planner asked for more workers than the setting allows — the plan is fundamentally misaligned with configuration.

**2. Check concurrent worker usage.**

Workers are a shared resource. If other queries were running in parallel at the same time, they may have consumed the budget:

```sql
SELECT pid, query, backend_type
FROM pg_stat_activity
WHERE backend_type = 'parallel worker';
```

**3. Raise the limits if appropriate.**

```sql
-- Raise per-Gather limit to match what the planner wants
ALTER SYSTEM SET max_parallel_workers_per_gather = 4;

-- Raise global limit if overall parallelism budget is too low
ALTER SYSTEM SET max_parallel_workers = 8;

-- Reload config (no restart needed for these settings)
SELECT pg_reload_conf();
```

Then re-run the query and confirm `Workers Launched` matches `Workers Planned`.

**4. Check OS limits.**

If workers fail to launch entirely (`Workers Launched: 0`), check OS-level process limits:

```bash
ulimit -u   # max user processes
```

Also check PostgreSQL logs for messages like `could not fork worker process`.

---

## Known limitations and false positives

**LIMIT queries.** When a query has `LIMIT`, the Gather node may stop workers early as soon as enough rows are collected. Workers that were never needed may show as "not launched" even though the plan executed efficiently. Look at `Actual Rows` on the Gather node — if it equals the LIMIT and is much smaller than `Plan Rows`, early termination is the likely explanation.

**Transient load spikes.** The worker shortage may have been caused by a concurrent load spike that has since resolved. Re-running the query may show full parallelism. If the finding is intermittent, focus on tuning `max_parallel_workers` to accommodate peak concurrent load.

**`Workers Launched` counts only background workers.** The leader process always participates in parallel execution but is not counted in `Workers Launched`. A plan with `Workers Planned: 4` and `Workers Launched: 4` has 5 processes total.

---

## Related rules

- **[RowEstimateMismatch](row_estimate_mismatch.md)** — when workers don't launch, the planner's row estimates (which assumed full parallelism) are off. This can compound with any existing row estimate mismatch.
- **[SortSpill](sort_spill.md)** — parallel sort operations size their memory allocation based on the number of workers. Fewer workers than planned means each worker sorts a larger chunk, increasing the chance of a spill.
- **[HashJoinSpill](hash_join_spill.md)** — same as SortSpill: parallel hash joins allocate work_mem per worker. With fewer workers, each worker's hash table is larger and more likely to spill.
