# pgexplain

A Go library that parses PostgreSQL `EXPLAIN (ANALYZE, FORMAT JSON)` output into typed structs and runs a rule-based advisor that emits human-readable performance suggestions.

```
[WARN] sequential scan on "orders" discards 8332x more rows than it returns
  detail:     PostgreSQL read 100000 rows from "orders" but only 12 matched
              (customer_id = 42) (8332 rows discarded per row returned).
  suggestion: Add an index on "orders" to support the filter (customer_id = 42).
              Run EXPLAIN (ANALYZE, BUFFERS) after adding the index to confirm it is used.
```

---

## What is pgexplain?

PostgreSQL's query planner produces a detailed execution plan for every query. Reading and interpreting that plan — especially under pressure, at scale, or in automated pipelines — is hard. `pgexplain` does it programmatically.

It has two responsibilities:

1. **Parse** `EXPLAIN (ANALYZE, FORMAT JSON)` output into a typed Go plan tree. Every node, cost, timing, and I/O stat is accessible as a real struct field — no JSON wrangling, no string parsing, no guessing at nil.

2. **Advise** by running a set of rules over the parsed tree. Each rule understands one PostgreSQL concept (sequential scans, row estimate mismatch, hash join spills, etc.) and emits a structured `Finding` with a message, a detailed explanation, and a concrete suggestion.

There is no good Go equivalent. `pgexplain` fills that gap.

---

## Who is it for?

`pgexplain` is designed to be embedded in tools that run queries and care about their plans:

- **Migration runners** — parse the plan of every migration query and fail if a rule fires, before it reaches production
- **CI pipelines** — gate pull requests on query plan quality, not just correctness
- **Slow query loggers** — annotate every slow query log entry with actionable suggestions, not just the raw plan
- **Developer CLIs** — surface plan warnings in the local development loop, where they're cheapest to fix

---

## Installation

```bash
go get github.com/pgexplain/pgexplain
```

Requires Go 1.21+.

---

## Quick start

```go
package main

import (
    "fmt"

    "github.com/pgexplain/pgexplain/advisor"
    "github.com/pgexplain/pgexplain/parser"
    "github.com/pgexplain/pgexplain/rules"
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
    )

    for _, f := range adv.Analyze(plan) {
        node, _ := plan.NodeByID(f.NodeID)
        fmt.Printf("[%s] %s\n", f.Severity, f.Message)
        fmt.Printf("  node:       %s on %q\n", node.NodeType, *node.RelationName)
        fmt.Printf("  detail:     %s\n", f.Detail)
        fmt.Printf("  suggestion: %s\n\n", f.Suggestion)
    }
}
```

---

## How it works

> Full walkthrough coming in v0.4 alongside the CLI tool. The short version:

`parser.Parse()` decodes the JSON array that PostgreSQL emits and builds a typed `*Plan` tree. Every node in the tree is stamped with a unique integer ID (depth-first pre-order, root = 1). `advisor.Analyze()` walks the tree iteratively and applies every registered `Rule` to each node. Rules return zero or more `Finding` values. Findings carry the node ID so callers can look up the full node via `plan.NodeByID()`.

---

## API reference

### Parsing

```go
plan, err := parser.Parse([]byte(explainJSON))
```

`plan.Node` is the root of the plan tree. `plan.NodeByID(id)` lets you retrieve any node by ID.

### Findings

Each `advisor.Finding` contains:

| Field | Type | Description |
|---|---|---|
| `Severity` | `advisor.Severity` | `Info`, `Warn`, or `Error` |
| `NodeID` | `int` | ID of the node that triggered this finding |
| `NodeType` | `string` | Node type (e.g. `"Seq Scan"`) — convenience copy for display |
| `Message` | `string` | Short one-line summary |
| `Detail` | `string` | Longer explanation of why this is a problem |
| `Suggestion` | `string` | What to do about it |

### Writing your own rule

Implement `advisor.Rule`:

```go
type Rule interface {
    Check(node parser.Node) []Finding
}
```

Pass it to `advisor.New()` alongside the built-in rules. Your rule will be called once for every node in the tree.

---

## Supported rules

| Rule | Constructor | Detects | Docs |
|---|---|---|---|
| SeqScan | `rules.SeqScan()` | Sequential scan that discards far more rows than it returns | [docs/rules/seq_scan.md](docs/rules/seq_scan.md) |

More rules are being added — see [Roadmap](#roadmap).

---

## Roadmap

| # | Rule | Concept it teaches |
|---|---|---|
| 1 | `SeqScan` ✅ | What a sequential scan is and when it hurts |
| 2 | `RowEstimateMismatch` | Cost model, planner statistics, Plan Rows vs Actual Rows |
| 3 | `HashJoinSpill` | Join strategies, `work_mem`, temp block I/O |
| 4 | `NestedLoopLarge` | When nested loops hurt, N+1 suspicion |
| 5 | `MissingIndexOnlyScan` | Index Scan vs Index Only Scan, visibility map |
| 6 | `SortSpill` | Sort strategies, top-N optimization |
| 7 | `ParallelNotLaunched` | Gather nodes, Workers Planned vs Launched |

---

## License

MIT
