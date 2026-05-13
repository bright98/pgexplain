# Rule: SeqScan

**Package:** `rules`  
**Constructor:** `rules.SeqScan(...SeqScanOption)`  
**Severity:** `Warn`

---

## What is a Sequential Scan?

When PostgreSQL executes a query, it chooses an *access method* for each table — the strategy it uses to retrieve the rows it needs. The simplest access method is the **Sequential Scan** (`Seq Scan`): read every row in the table, in physical storage order, and evaluate the `WHERE` clause on each one.

You can think of it like searching a paper phonebook by reading every entry from the first page to the last. It always finds what you're looking for, but it does the same amount of work regardless of whether you're looking for one person or a thousand.

In a query plan, a Seq Scan looks like this:

```
-> Seq Scan on orders  (cost=0.00..1849.00 rows=15 width=72)
                       (actual time=0.042..18.721 rows=12 loops=1)
     Filter: (customer_id = 42)
     Rows Removed by Filter: 99988
```

Three things to notice:
- **`Filter`** — the `WHERE` clause was evaluated row by row inside the scan
- **`Rows Removed by Filter: 99988`** — nearly 100,000 rows were read and discarded
- **`actual rows=12`** — only 12 rows were actually needed

The scan read 100,000 rows to return 12. That is a 8,332:1 discard ratio.

---

## When is a Sequential Scan fine?

Sequential Scans are not always bad. The PostgreSQL planner is smart — it chooses Seq Scan deliberately when it is the right call:

- **Small tables.** For a table with 200 rows, reading all 200 is faster than traversing a B-tree index (which has its own I/O cost). The planner uses statistics to estimate the crossover point.
- **Queries that need most of the table.** If your query returns 80% of a table's rows, an index scan would be slower — it would jump around the table randomly (expensive random I/O) rather than reading it sequentially.
- **No filter.** `SELECT * FROM t` with no `WHERE` clause is a full read by design. There is nothing to optimize.

This rule only flags Seq Scans that have a `Filter` and a high discard ratio — cases where the planner chose Seq Scan but an index would have been much cheaper.

---

## What does this rule check?

The rule looks at three fields from the EXPLAIN output:

| Field | Meaning |
|---|---|
| `Node Type` | Must be `"Seq Scan"` |
| `Rows Removed by Filter` | How many rows were read and thrown away |
| `Actual Rows` | How many rows the scan actually returned |

**The check:**

```
ratio = Rows Removed by Filter / Actual Rows

if ratio >= minFilterRatio  →  emit Warn finding
```

**Default threshold:** `minFilterRatio = 10`  
A scan that throws away 10 rows for every 1 it returns is flagged. Raise this if you want less noise; lower it to catch more marginal cases.

**Special case — zero actual rows:**  
If the query matched nothing (`Actual Rows = 0`), division is undefined. Instead the rule checks whether `Rows Removed by Filter >= 1000` — meaning the scan did significant work for nothing.

**No-filter scans are ignored:**  
If `Rows Removed by Filter` is absent in the plan, this is a full table read with no `WHERE` predicate. The rule stays silent — there is nothing to optimize.

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
    rules.SeqScan(), // default: warn when ratio >= 10
)

for _, f := range adv.Analyze(plan) {
    node, _ := plan.NodeByID(f.NodeID)
    fmt.Println(f.Message)
    fmt.Println(f.Detail)
    fmt.Println(f.Suggestion)
}
```

---

## Options

### `WithMinFilterRatio(ratio float64)`

Sets the threshold for the discard ratio. The rule fires when:

```
Rows Removed by Filter / Actual Rows >= ratio
```

```go
// Default — warn at 10x or more
rules.SeqScan()

// Stricter — catch scans with a 5x or greater discard ratio
rules.SeqScan(rules.WithMinFilterRatio(5))

// Looser — only flag truly egregious scans
rules.SeqScan(rules.WithMinFilterRatio(100))
```

**Choosing a threshold:**  
- `10` is a reasonable default for most applications. A 10:1 discard ratio on a large table almost always benefits from an index.
- `5` is appropriate for latency-sensitive services where even moderate waste matters.
- Very low values (< 3) will produce a lot of noise on tables where a Seq Scan is genuinely close to optimal.

---

## Sample finding

For the query `SELECT * FROM orders WHERE customer_id = 42` against a 100,000-row table with no index on `customer_id`:

```
Severity:   WARN
NodeID:     1
NodeType:   Seq Scan

Message:    sequential scan on "orders" discards 8332x more rows than it returns

Detail:     PostgreSQL read 100000 rows from "orders" but only 12 matched
            (customer_id = 42) (8332 rows discarded per row returned).
            An index on the filtered column(s) would allow PostgreSQL to
            skip non-matching rows.

Suggestion: Add an index on "orders" to support the filter (customer_id = 42).
            Run EXPLAIN (ANALYZE, BUFFERS) after adding the index to confirm
            it is used.
```

---

## What to do when this fires

1. **Check if an index already exists.** Run `\d orders` in psql. It is possible an index exists but the planner chose not to use it (e.g. the table is too small, or statistics are stale — run `ANALYZE orders` and re-check).

2. **Add an index on the filtered column(s).** For the example above:
   ```sql
   CREATE INDEX ON orders (customer_id);
   ```
   For multi-column filters, a composite index may be better — the column with the highest selectivity (fewest distinct values relative to table size) should usually come first.

3. **Verify the index is used.** After adding the index, re-run `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)` and confirm the plan changed to an `Index Scan` or `Index Only Scan`. If it did not, the planner may still prefer Seq Scan (the table grew smaller since stats were last collected, or the query is on a replica with different statistics).

4. **Consider `work_mem` and partial indexes.** If the filter is always on a fixed value range (e.g. `status = 'pending'`), a [partial index](https://www.postgresql.org/docs/current/indexes-partial.html) can be significantly smaller and faster than a full index.

---

## Known limitations and false positives

**Small tables.** The rule does not know the absolute size of the table — only the row counts reported in the plan. A table with 50 rows and a 20:1 discard ratio is not a real problem, but this rule will flag it. If you have small lookup tables that produce noisy findings, raise `minFilterRatio` or filter findings by `node.TotalCost` in your own code.

**Parallel Seq Scans.** On partitioned tables, PostgreSQL may launch parallel workers each doing a Seq Scan on a subset of the data. Each worker's `Rows Removed by Filter` reflects only its subset, so the ratio may look different from a single-worker scan. The finding is still meaningful, but the row counts in the detail message reflect one worker, not the total.

**Statistics lag.** Immediately after a large bulk insert, `pg_statistic` may not reflect the new table size. The planner might choose Seq Scan because it thinks the table is small, even though the discard ratio looks high. Run `ANALYZE` before drawing conclusions.

---

## Related rules

- **[RowEstimateMismatch](row_estimate_mismatch.md)** — if the planner estimated 15 rows but got 12,000, the planner's choice of Seq Scan may have been based on bad statistics. That rule surfaces the estimation error itself.
- **[MissingIndexOnlyScan](missing_index_only_scan.md)** — if the table has an index and the plan shows an `Index Scan` (not `Index Only Scan`), you may be able to eliminate the heap fetch entirely with a covering index.
