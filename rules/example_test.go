package rules_test

import (
	"fmt"

	"github.com/pgexplain/pgexplain/advisor"
	"github.com/pgexplain/pgexplain/parser"
	"github.com/pgexplain/pgexplain/rules"
)

// ExampleSeqScan demonstrates end-to-end usage of the SeqScan rule.
//
// The plan below represents a query like:
//
//	SELECT * FROM orders WHERE customer_id = 42
//
// PostgreSQL chose a Seq Scan and read all 100,000 rows in the table,
// but only 12 matched the filter — a ratio of 8332:1. The rule flags
// this as a candidate for an index on orders(customer_id).
func ExampleSeqScan() {
	explainJSON := []byte(`[{
		"Plan": {
			"Node Type": "Seq Scan",
			"Parallel Aware": false,
			"Relation Name": "orders",
			"Alias": "orders",
			"Startup Cost": 0.00,
			"Total Cost": 1849.00,
			"Plan Rows": 15,
			"Plan Width": 72,
			"Actual Startup Time": 0.042,
			"Actual Total Time": 18.721,
			"Actual Rows": 12,
			"Actual Loops": 1,
			"Filter": "(customer_id = 42)",
			"Rows Removed by Filter": 99988
		},
		"Planning Time": 0.123,
		"Execution Time": 18.854
	}]`)

	plan, err := parser.Parse(explainJSON)
	if err != nil {
		panic(err)
	}

	adv := advisor.New(
		rules.SeqScan(), // default: warn when ratio >= 10
	)

	for _, f := range adv.Analyze(plan) {
		node, _ := plan.NodeByID(f.NodeID)
		fmt.Printf("[%s] %s\n", f.Severity, f.Message)
		fmt.Printf("  node:       %s on %q\n", node.NodeType, *node.RelationName)
		fmt.Printf("  detail:     %s\n", f.Detail)
		fmt.Printf("  suggestion: %s\n", f.Suggestion)
	}

	// Output:
	// [WARN] sequential scan on "orders" discards 8332x more rows than it returns
	//   node:       Seq Scan on "orders"
	//   detail:     PostgreSQL read 100000 rows from "orders" but only 12 matched (customer_id = 42) (8332 rows discarded per row returned). An index on the filtered column(s) would allow PostgreSQL to skip non-matching rows.
	//   suggestion: Add an index on "orders" to support the filter (customer_id = 42). Run EXPLAIN (ANALYZE, BUFFERS) after adding the index to confirm it is used.
}

// ExampleSeqScan_withCustomThreshold shows how to lower the filter ratio
// threshold to catch less severe (but still wasteful) sequential scans.
func ExampleSeqScan_withCustomThreshold() {
	explainJSON := []byte(`[{
		"Plan": {
			"Node Type": "Seq Scan",
			"Parallel Aware": false,
			"Relation Name": "events",
			"Alias": "events",
			"Startup Cost": 0.00,
			"Total Cost": 540.00,
			"Plan Rows": 100,
			"Plan Width": 48,
			"Actual Startup Time": 0.011,
			"Actual Total Time": 4.201,
			"Actual Rows": 100,
			"Actual Loops": 1,
			"Filter": "(type = 'click')",
			"Rows Removed by Filter": 700
		},
		"Planning Time": 0.055,
		"Execution Time": 4.250
	}]`)

	plan, err := parser.Parse(explainJSON)
	if err != nil {
		panic(err)
	}

	// Default threshold (10x) would stay silent here — ratio is only 7x.
	// Lowering to 5x catches it.
	adv := advisor.New(
		rules.SeqScan(rules.WithMinFilterRatio(5)),
	)

	for _, f := range adv.Analyze(plan) {
		fmt.Printf("[%s] %s\n", f.Severity, f.Message)
	}

	// Output:
	// [WARN] sequential scan on "events" discards 7x more rows than it returns
}

// ExampleRowEstimateMismatch demonstrates the RowEstimateMismatch rule on a
// Seq Scan where the planner expected 15 rows but got 12,000 — an 800x underestimate.
//
// This kind of error typically means statistics are stale (ANALYZE hasn't run
// recently) or the column has a non-uniform distribution the planner can't model.
func ExampleRowEstimateMismatch() {
	explainJSON := []byte(`[{
		"Plan": {
			"Node Type": "Seq Scan",
			"Parallel Aware": false,
			"Relation Name": "orders",
			"Alias": "orders",
			"Startup Cost": 0.00,
			"Total Cost": 1849.00,
			"Plan Rows": 15,
			"Plan Width": 72,
			"Actual Startup Time": 0.042,
			"Actual Total Time": 18.721,
			"Actual Rows": 12000,
			"Actual Loops": 1,
			"Filter": "(status = 'pending')"
		},
		"Planning Time": 0.123,
		"Execution Time": 18.854
	}]`)

	plan, err := parser.Parse(explainJSON)
	if err != nil {
		panic(err)
	}

	adv := advisor.New(
		rules.RowEstimateMismatch(),
	)

	for _, f := range adv.Analyze(plan) {
		fmt.Printf("[%s] %s\n", f.Severity, f.Message)
		fmt.Printf("  detail:     %s\n", f.Detail)
		fmt.Printf("  suggestion: %s\n", f.Suggestion)
	}

	// Output:
	// [WARN] row estimate for Seq Scan was off by 800x (underestimate: planned 15, got 12000)
	//   detail:     The planner estimated 15 rows but this node produced 12000 (loops=1). A 800x underestimate can cause the planner to choose the wrong join strategy, misallocate work_mem, or produce a suboptimal join order.
	//   suggestion: Run ANALYZE on the tables involved in this node to refresh planner statistics. If the mismatch persists, consider raising the statistics target for the relevant columns: ALTER TABLE <table> ALTER COLUMN <column> SET STATISTICS 500;
}

// ExampleRowEstimateMismatch_withLoops shows that the rule correctly accounts
// for Actual Loops. Here an Index Scan inside a Nested Loop executes 100 times,
// each returning 50 rows — a true total of 5000 vs the planner's estimate of 10.
func ExampleRowEstimateMismatch_withLoops() {
	explainJSON := []byte(`[{
		"Plan": {
			"Node Type": "Nested Loop",
			"Parallel Aware": false,
			"Startup Cost": 0.42,
			"Total Cost": 9876.00,
			"Plan Rows": 1000,
			"Plan Width": 80,
			"Actual Startup Time": 0.031,
			"Actual Total Time": 120.4,
			"Actual Rows": 5000,
			"Actual Loops": 1,
			"Plans": [
				{
					"Node Type": "Seq Scan",
					"Parent Relationship": "Outer",
					"Parallel Aware": false,
					"Relation Name": "customers",
					"Alias": "c",
					"Startup Cost": 0.00,
					"Total Cost": 25.00,
					"Plan Rows": 100,
					"Plan Width": 36,
					"Actual Startup Time": 0.009,
					"Actual Total Time": 0.321,
					"Actual Rows": 100,
					"Actual Loops": 1
				},
				{
					"Node Type": "Index Scan",
					"Parent Relationship": "Inner",
					"Parallel Aware": false,
					"Relation Name": "orders",
					"Alias": "o",
					"Index Name": "orders_customer_id_idx",
					"Startup Cost": 0.42,
					"Total Cost": 12.47,
					"Plan Rows": 10,
					"Plan Width": 72,
					"Actual Startup Time": 0.031,
					"Actual Total Time": 0.038,
					"Actual Rows": 50,
					"Actual Loops": 100
				}
			]
		},
		"Planning Time": 0.201,
		"Execution Time": 122.5
	}]`)

	plan, err := parser.Parse(explainJSON)
	if err != nil {
		panic(err)
	}

	adv := advisor.New(
		rules.RowEstimateMismatch(),
	)

	for _, f := range adv.Analyze(plan) {
		fmt.Printf("[%s] %s\n", f.Severity, f.Message)
	}

	// Output:
	// [WARN] row estimate for Index Scan was off by 500x (underestimate: planned 10, got 5000)
}
