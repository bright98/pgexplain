# pgexplain

<p align="center">
  <img src="https://raw.githubusercontent.com/Bright98/pgexplain/master/docs/pgexplain.png" alt="pgexplain" width="320">
</p>

A Go library and CLI that parse PostgreSQL `EXPLAIN (ANALYZE, FORMAT JSON)` output and surface actionable performance findings.

```
$ pgexplain plan.json

[WARN]  sequential scan on "orders" discards 8332x more rows than it returns
  node:       Seq Scan (ID 1)
  detail:     PostgreSQL read 100000 rows from "orders" but only 12 matched
              (customer_id = 42) (8332 rows discarded per row returned).
  suggestion: Add an index on "orders" to support the filter (customer_id = 42).
              Run EXPLAIN (ANALYZE, BUFFERS) after adding the index to confirm it is used.

1 finding: 0 error(s), 1 warning(s), 0 info
```

---

## What is pgexplain?

PostgreSQL's query planner produces a detailed execution plan for every query. Reading and interpreting that plan — especially under pressure, at scale, or in automated pipelines — is hard. `pgexplain` does it programmatically.

It has two responsibilities:

1. **Parse** `EXPLAIN (ANALYZE, FORMAT JSON)` output into a typed Go plan tree. Every node, cost, timing, and I/O stat is accessible as a real struct field — no JSON wrangling, no string parsing, no guessing at nil.

2. **Advise** by running a set of rules over the parsed tree. Each rule understands one PostgreSQL concept (sequential scans, row estimate mismatch, hash join spills, etc.) and emits a structured `Finding` with a message, a detailed explanation, and a concrete suggestion.

---

## Who is it for?

- **Migration runners** — parse the plan of every migration query and fail if a rule fires, before it reaches production
- **CI pipelines** — gate pull requests on query plan quality, not just correctness
- **Slow query loggers** — annotate every slow query log entry with actionable suggestions, not just the raw plan
- **Developer CLIs** — surface plan warnings in the local development loop, where they're cheapest to fix

---

## CLI

### Install

```bash
go install github.com/Bright98/pgexplain/cmd/pgexplain@latest
```

Requires Go 1.21+.

### Usage

```bash
# Pipe directly from psql
psql -U myuser -d mydb \
  -c "EXPLAIN (ANALYZE, FORMAT JSON) SELECT * FROM orders WHERE customer_id = 42" \
  | pgexplain

# Read from a saved plan file
pgexplain plan.json

# Machine-readable output for CI tooling
pgexplain --format=json plan.json
```

### Exit codes

| Code | Meaning |
|------|---------|
| `0` | No findings, or only `Info` findings |
| `1` | At least one `Warn` or `Error` finding |
| `2` | Invalid input or parse error |

This makes `pgexplain` a drop-in CI gate:

```bash
pgexplain plan.json || exit 1
```

---

## Library

### Install

```bash
go get github.com/Bright98/pgexplain
```

### Quick start

```go
package main

import (
    "fmt"

    "github.com/Bright98/pgexplain/advisor"
    "github.com/Bright98/pgexplain/parser"
    "github.com/Bright98/pgexplain/rules"
)

func main() {
    // explainJSON is the raw output of:
    //   EXPLAIN (ANALYZE, FORMAT JSON) SELECT * FROM orders WHERE customer_id = 42
    plan, err := parser.Parse(explainJSON)
    if err != nil {
        panic(err)
    }

    adv := advisor.New(
        rules.SeqScan(),
        rules.RowEstimateMismatch(),
        rules.HashJoinSpill(),
        rules.NestedLoopLarge(),
        rules.MissingIndexOnlyScan(),
        rules.SortSpill(),
        rules.TopNHeapsort(),
        rules.ParallelNotLaunched(),
    )

    for _, f := range adv.Analyze(plan) {
        fmt.Printf("[%s] %s\n", f.Severity, f.Message)
        fmt.Printf("  detail:     %s\n", f.Detail)
        fmt.Printf("  suggestion: %s\n\n", f.Suggestion)
    }
}
```

### API reference

#### Parsing

```go
plan, err := parser.Parse([]byte(explainJSON))
```

`plan.Node` is the root of the plan tree. `plan.NodeByID(id)` retrieves any node by its ID.

#### Findings

Each `advisor.Finding` contains:

| Field | Type | Description |
|---|---|---|
| `Severity` | `advisor.Severity` | `Info`, `Warn`, or `Error` |
| `NodeID` | `int` | ID of the node that triggered this finding |
| `NodeType` | `string` | Node type (e.g. `"Seq Scan"`) |
| `Message` | `string` | Short one-line summary |
| `Detail` | `string` | Longer explanation of why this is a problem |
| `Suggestion` | `string` | What to do about it |

#### Writing your own rule

Implement `advisor.Rule`:

```go
type Rule interface {
    Check(node parser.Node) []Finding
}
```

Pass it to `advisor.New()` alongside the built-in rules. Your rule is called once for every node in the tree.

---

## How it works

`parser.Parse()` decodes the JSON array that PostgreSQL emits and builds a typed `*Plan` tree. Every node in the tree is stamped with a unique integer ID assigned in depth-first pre-order (root = 1).

`advisor.Analyze()` walks the tree with an explicit stack and applies every registered `Rule` to each node. Rules return zero or more `Finding` values. Findings carry the node ID so callers can look up the full node via `plan.NodeByID()`.

---

## Supported rules

| Rule | Constructor | Detects | Docs |
|---|---|---|---|
| SeqScan | `rules.SeqScan()` | Sequential scan that discards far more rows than it returns | [docs](docs/rules/seq_scan.md) |
| RowEstimateMismatch | `rules.RowEstimateMismatch()` | Planner row estimate diverges significantly from actual rows produced | [docs](docs/rules/row_estimate_mismatch.md) |
| HashJoinSpill | `rules.HashJoinSpill()` | Hash join spilled to disk because the hash table exceeded `work_mem` | [docs](docs/rules/hash_join_spill.md) |
| NestedLoopLarge | `rules.NestedLoopLarge()` | Nested Loop executed its inner side an excessive number of times | [docs](docs/rules/nested_loop_large.md) |
| MissingIndexOnlyScan | `rules.MissingIndexOnlyScan()` | Index Only Scan degraded by heap fetches — visibility map not up to date | [docs](docs/rules/missing_index_only_scan.md) |
| SortSpill | `rules.SortSpill()` | Sort node exceeded `work_mem` and wrote temporary data to disk | [docs](docs/rules/sort_spill.md) |
| TopNHeapsort | `rules.TopNHeapsort()` | top-N heapsort reads the full table when an index on the sort key could stop early | [docs](docs/rules/top_n_heapsort.md) |
| ParallelNotLaunched | `rules.ParallelNotLaunched()` | Gather node launched fewer workers than planned — parallelism was constrained at runtime | [docs](docs/rules/parallel_not_launched.md) |

---

## Roadmap

The following rules are planned for the next release:

| Rule | Detects |
|---|---|
| `MergeJoinUnsortedInputs` | Merge Join has explicit Sort children — an index on the join key would eliminate the sort overhead |
| `HighTempBlockIO` | Any node with high temporary block I/O (`TempReadBlocks` / `TempWrittenBlocks`) — catches disk spills beyond sort and hash join |
| `BitmapHeapRecheckOverhead` | Bitmap Heap Scan switched to lossy mode — bitmap exceeded `work_mem`, forcing a row-level recheck on every matched page |
| `CTEScanMaterialized` | CTE Scan over a large materialized result, especially when the same CTE is scanned multiple times inside a join |
| `IndexScanLowEfficiency` | Index Scan reads many blocks per row returned — signals heap fragmentation, dead tuples, or low index selectivity |

---

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for how to run tests, add a new rule, and the conventions the codebase follows.

---

## License

MIT
