# Rule: RowEstimateMismatch

**Package:** `rules`  
**Constructor:** `rules.RowEstimateMismatch(...RowEstimateMismatchOption)`  
**Severity:** `Warn`

---

## What is a row estimate mismatch?

Before PostgreSQL executes a query, its planner builds every possible execution strategy and assigns a **cost** to each one. To calculate cost, the planner needs to know roughly how many rows each node will produce — because cost depends on row count. The estimated row count is called **Plan Rows**.

These estimates come from **table statistics** collected by `ANALYZE` and stored in `pg_statistic`. The planner looks at each column's statistics — distinct value count, most common values, histogram buckets — and uses them to estimate how selective a filter will be. Multiply selectivity by table size and you get `Plan Rows`.

After the query runs, EXPLAIN shows the reality:

```
-> Hash Join  (cost=31.50..3025.10 rows=1509 width=104)
              (actual time=2.45..38.23 rows=12000 loops=1)
```

- `rows=1509` — the planner expected 1,509 rows
- `rows=12000` — the join actually produced 12,000 rows
- That is an **8× underestimate**

---

## Why does it matter?

The planner uses `Plan Rows` for every downstream decision:

**Join strategy.** A Hash Join builds a hash table in memory sized for the expected number of rows. If the real count is 100× larger, the hash table spills to disk — which is orders of magnitude slower. The planner might have chosen a different join strategy if it had known the true size.

**Join order.** With multiple joins, the planner tries to put the smallest intermediate result on the inner side of each join. Wrong estimates lead to wrong join ordering, which compounds with each additional table.

**Index vs Seq Scan.** If the planner thinks a table has 50 rows (because statistics are stale after a bulk insert), it may choose a Seq Scan. When the table actually has 500,000 rows, that choice is catastrophically expensive.

**work_mem allocation.** Sort and hash operations allocate memory proportional to their estimated input size. An underestimate means they run out of memory and spill to disk; an overestimate means they hold memory other queries could use.

A mismatch does not always cause visible harm — sometimes the chosen strategy happens to work fine anyway. But a large mismatch means the planner is operating on bad information, and the next query or the next week's growth may push it into a genuinely bad plan.

---

## The critical detail: Actual Rows is per loop

EXPLAIN reports `Actual Rows` **per loop execution**. Inside a Nested Loop, the inner node executes once for every row on the outer side. If the outer side has 100 rows, the inner node runs 100 times — and `Actual Rows` shows the average per execution.

The true total actual rows is:

```
true_actual_rows = Actual Rows × Actual Loops
```

This is the most commonly missed subtlety when reading EXPLAIN output. Compare this to `Plan Rows`, which is always the total. If you compare `Plan Rows` to `Actual Rows` directly without multiplying by loops, you will get a misleadingly small factor for nodes inside Nested Loops.

Example:

```
-> Index Scan on orders  (cost=0.42..12.47 rows=10 width=72)
                         (actual time=0.031..0.038 rows=50 loops=100)
```

- `Plan Rows = 10` — total estimate
- `Actual Rows = 50` — per loop
- `Actual Loops = 100`
- `true_actual_rows = 50 × 100 = 5000`
- Real factor: `5000 / 10 = 500×` — not `50 / 10 = 5×`

This rule always computes `true_actual_rows = Actual Rows × Actual Loops` before comparing.

---

## Common causes of row estimate mismatch

| Cause | Explanation |
|---|---|
| **Stale statistics** | `ANALYZE` hasn't run since a large bulk insert, update, or delete |
| **Non-uniform distribution** | 99% of `orders` rows have `status = 'active'`, but the planner assumes a uniform distribution unless the column is in `pg_statistic`'s MCV list |
| **Correlated columns** | `WHERE city = 'New York' AND state = 'NY'` — the planner treats columns as statistically independent, underestimating the combined selectivity |
| **Functions in WHERE** | `WHERE lower(email) = 'x@example.com'` — the planner cannot use column statistics for the transformed value and falls back to a generic 0.5% selectivity estimate |
| **Very high cardinality** | Columns with millions of distinct values may have sparse histogram buckets, leading to poor range query estimates |
| **Partitioned tables** | Statistics are per-partition; the root table's statistics may not reflect the partition that is actually scanned |

---

## What does this rule check?

The rule examines three fields:

| Field | Meaning |
|---|---|
| `Plan Rows` | The planner's estimated row count for this node |
| `Actual Rows` | Rows produced per loop execution (ANALYZE required) |
| `Actual Loops` | How many times this node executed |

**The check:**

```
true_actual_rows = Actual Rows × Actual Loops
factor = max(Plan Rows, true_actual_rows) / min(Plan Rows, true_actual_rows)

if max(Plan Rows, true_actual_rows) >= minRows   (default: 100)
and factor >= minEstimateFactor                  (default: 10)
  → emit Warn finding
```

The factor formula is **symmetric** — underestimating by 10× and overestimating by 10× produce the same factor value, because both mislead the planner in different but equally harmful ways.

**The minRows floor** prevents noise from tiny nodes. A 10× error when Plan Rows is 2 and true actual is 20 is not worth flagging — the planner's decision would have been the same either way. The rule skips the finding when the larger of the two sides is below `minRows`.

**Zero handling.** If one side is zero (e.g. `Plan Rows: 0`, which PostgreSQL sometimes emits for CTE scans), the denominator is clamped to 1 to avoid division by zero.

**ANALYZE required.** If EXPLAIN was run without ANALYZE, `Actual Rows` and `Actual Loops` are absent and the rule stays silent.

---

## How to use it

```go
import (
    "github.com/pgexplain/pgexplain/advisor"
    "github.com/pgexplain/pgexplain/parser"
    "github.com/pgexplain/pgexplain/rules"
)

plan, _ := parser.Parse(explainJSON)

adv := advisor.New(
    rules.RowEstimateMismatch(),
)

for _, f := range adv.Analyze(plan) {
    node, _ := plan.NodeByID(f.NodeID)
    fmt.Printf("[%s] %s\n", f.Severity, f.Message)
    fmt.Printf("  node:       %s (ID %d)\n", node.NodeType, node.ID)
    fmt.Printf("  detail:     %s\n", f.Detail)
    fmt.Printf("  suggestion: %s\n\n", f.Suggestion)
}
```

---

## Options

### `WithMinEstimateFactor(factor float64)`

The minimum error factor that triggers a warning. Default: **10**.

```go
rules.RowEstimateMismatch()                               // warn at 10x+
rules.RowEstimateMismatch(rules.WithMinEstimateFactor(50)) // only flag severe mismatches
rules.RowEstimateMismatch(rules.WithMinEstimateFactor(3))  // catch moderate mismatches
```

**Choosing a threshold:**
- `10` is a sensible default — a 10× error is an order of magnitude and almost always indicative of a real statistics problem.
- `50` or higher is appropriate if you only want to flag cases where the planner's choice of strategy was almost certainly wrong.
- Values below `5` will produce noise, especially on intermediate nodes whose estimates are derived from leaf node estimates and compound naturally.

### `WithMinRows(rows float64)`

The minimum row count for either side of the comparison. Default: **100**.

```go
rules.RowEstimateMismatch(rules.WithMinRows(500))  // ignore small nodes
rules.RowEstimateMismatch(rules.WithMinRows(1))    // flag everything, including tiny nodes
```

**Choosing a floor:**
- `100` avoids noise on small lookup tables and tiny intermediate results.
- Raise this if you only care about large-table query performance.
- Lower it if you are embedding this in a CI pipeline where even small estimates matter.

---

## Sample findings

**Underestimate** (plan=15, actual=12,000, loops=1):

```
Severity:   WARN
NodeID:     1
NodeType:   Seq Scan

Message:    row estimate for Seq Scan was off by 800x
            (underestimate: planned 15, got 12000)

Detail:     The planner estimated 15 rows but this node produced 12000 (loops=1).
            A 800x underestimate can cause the planner to choose the wrong join
            strategy, misallocate work_mem, or produce a suboptimal join order.

Suggestion: Run ANALYZE on the tables involved in this node to refresh planner
            statistics. If the mismatch persists, consider raising the statistics
            target for the relevant columns:
            ALTER TABLE <table> ALTER COLUMN <column> SET STATISTICS 500;
```

**Nested Loop case** (plan=10, actual=50 per loop, loops=100 → true_actual=5,000):

```
Message:    row estimate for Index Scan was off by 500x
            (underestimate: planned 10, got 5000)

Detail:     The planner estimated 10 rows but this node produced 5000 (loops=100).
            ...
```

---

## What to do when this fires

**1. Run ANALYZE.**
The most common cause is stale statistics. Run `ANALYZE <table>` on every table referenced in or near the flagged node, then re-run EXPLAIN. If the mismatch disappears, you just needed fresh statistics. Consider setting up autovacuum more aggressively for tables with frequent bulk writes.

**2. Raise the statistics target.**
PostgreSQL collects statistics at a configurable level of detail per column (default `statistics_target = 100`). For columns with highly non-uniform distributions or very high cardinality, raise the target:

```sql
ALTER TABLE orders ALTER COLUMN status SET STATISTICS 500;
ANALYZE orders;
```

Then re-run EXPLAIN and check if the estimate improved.

**3. Look for correlated columns.**
If you have a `WHERE` clause filtering on two or more columns that are correlated (e.g. `city` and `state`, or `user_id` and `account_id`), the planner multiplies their selectivities independently, underestimating how selective the combined filter is. PostgreSQL 10+ supports extended statistics for this:

```sql
CREATE STATISTICS orders_city_state ON city, state FROM orders;
ANALYZE orders;
```

**4. Check for functions in WHERE.**
`WHERE lower(email) = '...'` bypasses column statistics entirely. Consider a functional index instead:

```sql
CREATE INDEX ON users (lower(email));
```

This also allows the planner to use index statistics for the expression.

---

## Known limitations and false positives

**Derived node mismatch.** The estimate for a Hash Join or Sort is derived from its children's estimates. If a leaf node has a bad estimate, all ancestor nodes will too. You may see multiple findings on a single query — one per affected node. The root cause is always at a leaf scan node; look there first.

**Parallel workers.** In parallel plans, `Actual Rows` and `Actual Loops` are reported per worker, not in aggregate. The true total across all workers is higher. The rule may undercount the real factor for parallel nodes.

**ANALYZE timing.** Statistics are a snapshot. If you run ANALYZE right before re-checking, the mismatch may disappear even if the underlying data distribution problem still exists. Check if autovacuum is running frequently enough for the table's write rate.

---

## Related rules

- **[SeqScan](seq_scan.md)** — if a bad row estimate caused the planner to choose a Seq Scan, the SeqScan rule will also fire, pointing at the high filter discard ratio. The two findings together paint the full picture: the planner chose the wrong access method because it underestimated row count.
- **HashJoinSpill** *(coming soon)* — a row estimate underestimate on the inner side of a Hash Join often causes the hash table to spill to disk. RowEstimateMismatch tells you the estimate was wrong; HashJoinSpill tells you the consequence.
