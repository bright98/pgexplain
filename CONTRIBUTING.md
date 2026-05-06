# Contributing to pgexplain

Thank you for your interest in contributing. This document covers the essentials: how to run tests, how to add a new rule, and the conventions the codebase follows.

---

## Running the tests

```bash
go test ./...
```

All tests must pass before a pull request is merged. The CI pipeline runs `go test ./...` and `go build ./...` on every push and pull request.

To run tests for a single package:

```bash
go test ./rules/
go test ./parser/
go test ./advisor/
```

To run a single test or example by name:

```bash
go test ./rules/ -run TestSeqScan
go test ./rules/ -run ExampleHashJoinSpill -v
```

---

## Building the CLI

```bash
go build ./cmd/pgexplain/
./pgexplain --help
```

To install it to your `$GOPATH/bin`:

```bash
go install ./cmd/pgexplain/
```

---

## Project layout

```
pgexplain/
├── parser/          # JSON → typed Plan tree
├── advisor/         # Rule interface, Finding struct, Advisor walker
├── rules/           # One file per rule, plus example_test.go
│   └── example_test.go
├── cmd/pgexplain/   # CLI binary
├── testdata/        # EXPLAIN JSON fixtures used by parser tests
└── docs/rules/      # One markdown doc per rule
```

---

## Adding a new rule

Each rule lives in its own file: `rules/<name>.go`. Adding a rule requires:

1. **`rules/<snake_case_name>.go`** — the rule implementation
2. **`rules/<snake_case_name>_test.go`** — table-driven tests (`package rules_test`)
3. **An example** added to `rules/example_test.go`
4. **`docs/rules/<snake_case_name>.md`** — full documentation
5. **Updates to `README.md`** — add to the supported rules table
6. **Cross-links** — update related rule docs to link to the new one
7. **Register in the CLI** — add to `buildAdvisor()` in `cmd/pgexplain/main.go`

### Rule interface

```go
type Rule interface {
    Check(node parser.Node) []Finding
}
```

`Check` is called once for every node in the plan tree. Return `nil` when the rule is silent for that node.

### Conventions

**Node type check first.** Return `nil` immediately if the node type does not match — avoid doing any work for irrelevant nodes.

**Nil means absent.** All optional `Node` fields use pointer types (`*float64`, `*string`, etc.). Nil means the field was not present in the EXPLAIN JSON — not that its value is zero. Always nil-check before dereferencing.

**Functional options for thresholds.** If the rule has configurable parameters, expose them via the functional options pattern:

```go
type MyRuleOption func(*myRule)

func WithSomeThreshold(v float64) MyRuleOption {
    return func(r *myRule) { r.threshold = v }
}

func MyRule(opts ...MyRuleOption) advisor.Rule {
    r := &myRule{threshold: defaultThreshold}
    for _, o := range opts {
        o(r)
    }
    return r
}
```

**Severity.** Use `advisor.Warn` by default. Use `advisor.Error` only for clearly catastrophic plan shapes (e.g. O(n²) full table scans inside a Nested Loop). Use `advisor.Info` for hints where the finding is not a confirmed problem (e.g. `TopNHeapsort`).

**Finding.NodeID must be node.ID.** Always report findings on the node being inspected, not on a child or parent.

**Extract detail and suggestion into named functions.** Keep `Check` focused on the condition; put message construction in `buildXxxDetail` and `buildXxxSuggestion` helper functions.

**Use `parser.NodeXxx` constants.** Never use raw string literals like `"Seq Scan"` in rule code.

### Test conventions

Tests live in `package rules_test` (black-box). Use table-driven tests with `t.Run`. The following pointer helpers are available across all test files in the package:

```go
pf64(v float64) *float64
pi64(v int64)   *int64
ps(v string)    *string
pint(v int)     *int
```

Every rule must have test cases for:
- Fires above the threshold (canonical case)
- Fires at exactly the threshold (boundary)
- Silent just below the threshold
- Silent when ANALYZE was not run (Actual* fields nil)
- Silent on the wrong node type
- Custom threshold option (fires above, silent below)
- Any zero/nil edge cases specific to the rule

---

## Code style

- Standard `gofmt` formatting — run `gofmt -w .` before committing
- No external dependencies beyond the Go standard library (library and CLI both)
- Errors wrapped with `fmt.Errorf("...: %w", err)` at package boundaries
- No `panic` in library code

---

## Pull request checklist

- [ ] `go test ./...` passes
- [ ] `go build ./...` passes  
- [ ] New rule registered in `cmd/pgexplain/main.go` → `buildAdvisor()`
- [ ] Rule documented in `docs/rules/<name>.md`
- [ ] README supported rules table updated
- [ ] Related rule docs updated with cross-links
- [ ] No new external dependencies introduced
