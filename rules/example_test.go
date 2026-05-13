package rules_test

import (
	"fmt"
	"os"

	"github.com/bright98/pgexplain/advisor"
	"github.com/bright98/pgexplain/parser"
	"github.com/bright98/pgexplain/rules"
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

// ExampleHashJoinSpill demonstrates the HashJoinSpill rule on a plan where
// the hash table for a join exceeded work_mem and spilled to disk across 4 batches.
//
// The Hash node (inner child of Hash Join) is where PostgreSQL builds the hash
// table. Hash Batches > 1 means it could not fit in work_mem and batched to disk.
func ExampleHashJoinSpill() {
	explainJSON := []byte(`[{
		"Plan": {
			"Node Type": "Hash Join",
			"Parallel Aware": false,
			"Startup Cost": 31.50,
			"Total Cost": 3025.10,
			"Plan Rows": 1509,
			"Plan Width": 104,
			"Actual Startup Time": 2.451,
			"Actual Total Time": 38.234,
			"Actual Rows": 1842,
			"Actual Loops": 1,
			"Hash Cond": "(o.customer_id = c.id)",
			"Plans": [
				{
					"Node Type": "Seq Scan",
					"Parent Relationship": "Outer",
					"Parallel Aware": false,
					"Relation Name": "orders",
					"Alias": "o",
					"Startup Cost": 0.00,
					"Total Cost": 2846.00,
					"Plan Rows": 100000,
					"Plan Width": 72,
					"Actual Startup Time": 0.012,
					"Actual Total Time": 22.100,
					"Actual Rows": 100000,
					"Actual Loops": 1
				},
				{
					"Node Type": "Hash",
					"Parent Relationship": "Inner",
					"Parallel Aware": false,
					"Startup Cost": 25.00,
					"Total Cost": 25.00,
					"Plan Rows": 520,
					"Plan Width": 36,
					"Actual Startup Time": 2.431,
					"Actual Total Time": 2.431,
					"Actual Rows": 523,
					"Actual Loops": 1,
					"Hash Buckets": 1024,
					"Hash Batches": 4,
					"Peak Memory Usage": 4096
				}
			]
		},
		"Planning Time": 0.892,
		"Execution Time": 46.203
	}]`)

	plan, err := parser.Parse(explainJSON)
	if err != nil {
		panic(err)
	}

	adv := advisor.New(
		rules.HashJoinSpill(),
	)

	for _, f := range adv.Analyze(plan) {
		node, _ := plan.NodeByID(f.NodeID)
		fmt.Printf("[%s] %s\n", f.Severity, f.Message)
		fmt.Printf("  node ID:  %d (%s)\n", node.ID, node.NodeType)
		fmt.Printf("  batches:  %d\n", *node.HashBatches)
		fmt.Printf("  peak mem: %dkB\n", *node.PeakMemoryUsage)
	}

	// Output:
	// [WARN] hash join spilled to disk across 4 batches (peak memory: 4096kB per batch)
	//   node ID:  3 (Hash)
	//   batches:  4
	//   peak mem: 4096kB
}

// ExampleNestedLoopLarge_indexScan shows a Nested Loop where the inner side is
// an Index Scan executed 1000 times — once per outer row. Severity is Warn
// because each probe is efficient, but 1000 probes may indicate a bad estimate.
func ExampleNestedLoopLarge_indexScan() {
	// nested_loop.json: orders (outer, 1000 rows) × order_items index scan (inner, 1000 loops)
	explainJSON, err := os.ReadFile("../testdata/nested_loop.json")
	if err != nil {
		panic(err)
	}

	plan, err := parser.Parse(explainJSON)
	if err != nil {
		panic(err)
	}

	adv := advisor.New(rules.NestedLoopLarge())

	for _, f := range adv.Analyze(plan) {
		fmt.Printf("[%s] %s\n", f.Severity, f.Message)
	}

	// Output:
	// [WARN] nested loop probed Index Scan on "order_items" 1000 times
}

// ExampleNestedLoopLarge_seqScan shows the Error case: the inner side is a Seq Scan,
// meaning PostgreSQL reads the entire inner table for every outer row — O(outer × inner).
func ExampleNestedLoopLarge_seqScan() {
	explainJSON := []byte(`[{
		"Plan": {
			"Node Type": "Nested Loop",
			"Parallel Aware": false,
			"Startup Cost": 0.00,
			"Total Cost": 25000.00,
			"Plan Rows": 1500,
			"Plan Width": 120,
			"Actual Startup Time": 0.021,
			"Actual Total Time": 892.341,
			"Actual Rows": 1500,
			"Actual Loops": 1,
			"Plans": [
				{
					"Node Type": "Seq Scan",
					"Parent Relationship": "Outer",
					"Parallel Aware": false,
					"Relation Name": "orders",
					"Alias": "o",
					"Startup Cost": 0.00,
					"Total Cost": 250.00,
					"Plan Rows": 1000,
					"Plan Width": 72,
					"Actual Startup Time": 0.009,
					"Actual Total Time": 2.341,
					"Actual Rows": 1000,
					"Actual Loops": 1
				},
				{
					"Node Type": "Seq Scan",
					"Parent Relationship": "Inner",
					"Parallel Aware": false,
					"Relation Name": "order_items",
					"Alias": "i",
					"Startup Cost": 0.00,
					"Total Cost": 48.00,
					"Plan Rows": 3,
					"Plan Width": 48,
					"Actual Startup Time": 0.012,
					"Actual Total Time": 1.782,
					"Actual Rows": 3,
					"Actual Loops": 1000
				}
			]
		},
		"Planning Time": 0.198,
		"Execution Time": 894.123
	}]`)

	plan, err := parser.Parse(explainJSON)
	if err != nil {
		panic(err)
	}

	adv := advisor.New(rules.NestedLoopLarge())

	for _, f := range adv.Analyze(plan) {
		fmt.Printf("[%s] %s\n", f.Severity, f.Message)
	}

	// Output:
	// [ERROR] nested loop performed full table scan on "order_items" 1000 times
}

// ExampleMissingIndexOnlyScan demonstrates the MissingIndexOnlyScan rule on a
// plan where an Index Only Scan fetches 40% of rows from the heap.
//
// An Index Only Scan should serve all rows from the index without visiting the
// main table (heap). When VACUUM has not updated the visibility map, PostgreSQL
// must visit the heap to verify row visibility — defeating the purpose of the
// scan. Heap Fetches counts these visits.
func ExampleMissingIndexOnlyScan() {
	explainJSON := []byte(`[{
		"Plan": {
			"Node Type": "Index Only Scan",
			"Parallel Aware": false,
			"Relation Name": "users",
			"Alias": "u",
			"Index Name": "users_email_idx",
			"Startup Cost": 0.42,
			"Total Cost": 312.44,
			"Plan Rows": 500,
			"Plan Width": 36,
			"Actual Startup Time": 0.031,
			"Actual Total Time": 4.812,
			"Actual Rows": 500,
			"Actual Loops": 1,
			"Index Cond": "(email = 'x@example.com')",
			"Heap Fetches": 200
		},
		"Planning Time": 0.089,
		"Execution Time": 4.921
	}]`)

	plan, err := parser.Parse(explainJSON)
	if err != nil {
		panic(err)
	}

	adv := advisor.New(
		rules.MissingIndexOnlyScan(), // default: warn when >= 10% of rows hit the heap
	)

	for _, f := range adv.Analyze(plan) {
		fmt.Printf("[%s] %s\n", f.Severity, f.Message)
		fmt.Printf("  detail:     %s\n", f.Detail)
	}

	// Output:
	// [WARN] Index Only Scan on "users" (index: users_email_idx) fetched 40% of rows from the heap
	//   detail:     An Index Only Scan should serve rows entirely from the index without touching the heap. This node returned 500 rows but fetched 200 from the heap (40%). Heap fetches happen when the visibility map does not mark the page as all-visible, forcing PostgreSQL to verify row visibility in "users". This typically means VACUUM has not run recently enough on the table.
}

// ExampleSortSpill demonstrates the SortSpill rule on a plan where a Sort node
// exceeded work_mem and wrote temporary data to disk.
//
// PostgreSQL reports "Sort Method: external merge" and "Sort Space Type: Disk"
// when this happens. The rule fires on the Sort node and suggests a work_mem
// target calculated from the reported disk usage.
func ExampleSortSpill() {
	explainJSON := []byte(`[{
		"Plan": {
			"Node Type": "Sort",
			"Parallel Aware": false,
			"Startup Cost": 15420.44,
			"Total Cost": 17920.44,
			"Plan Rows": 100000,
			"Plan Width": 72,
			"Actual Startup Time": 892.341,
			"Actual Total Time": 1203.892,
			"Actual Rows": 100000,
			"Actual Loops": 1,
			"Sort Key": ["created_at DESC"],
			"Sort Method": "external merge",
			"Sort Space Used": 18432,
			"Sort Space Type": "Disk",
			"Plans": [{
				"Node Type": "Seq Scan",
				"Parent Relationship": "Outer",
				"Parallel Aware": false,
				"Relation Name": "events",
				"Alias": "events",
				"Startup Cost": 0.00,
				"Total Cost": 8681.00,
				"Plan Rows": 100000,
				"Plan Width": 72,
				"Actual Startup Time": 0.012,
				"Actual Total Time": 98.234,
				"Actual Rows": 100000,
				"Actual Loops": 1
			}]
		},
		"Planning Time": 0.145,
		"Execution Time": 1204.123
	}]`)

	plan, err := parser.Parse(explainJSON)
	if err != nil {
		panic(err)
	}

	adv := advisor.New(
		rules.SortSpill(),
	)

	for _, f := range adv.Analyze(plan) {
		fmt.Printf("[%s] %s\n", f.Severity, f.Message)
		fmt.Printf("  detail:     %s\n", f.Detail)
	}

	// Output:
	// [WARN] sort spilled to disk using external merge (18432 kB)
	//   detail:     The sort could not fit in work_mem and wrote temporary data to disk. PostgreSQL sorted chunks in memory, wrote them to temp files, then merged the files — reading and writing the sorted data at least twice. Disk usage for this sort: 18432 kB. An in-memory sort (quicksort) is significantly faster because it avoids all disk I/O.
}

// ExampleTopNHeapsort demonstrates the TopNHeapsort rule on a query that uses
// ORDER BY ... LIMIT 10 over a table of 100,000 rows.
//
// PostgreSQL chose top-N heapsort: it read all 100,000 rows and kept only the
// top 10 in a heap. An index on (created_at DESC) would allow an Index Scan to
// stop after 10 rows, making the query O(LIMIT) instead of O(table size).
func ExampleTopNHeapsort() {
	explainJSON := []byte(`[{
		"Plan": {
			"Node Type": "Sort",
			"Parallel Aware": false,
			"Startup Cost": 0.00,
			"Total Cost": 2891.34,
			"Plan Rows": 10,
			"Plan Width": 72,
			"Actual Startup Time": 312.451,
			"Actual Total Time": 312.453,
			"Actual Rows": 10,
			"Actual Loops": 1,
			"Sort Key": ["created_at DESC"],
			"Sort Method": "top-N heapsort",
			"Sort Space Used": 25,
			"Sort Space Type": "Memory",
			"Plans": [{
				"Node Type": "Seq Scan",
				"Parent Relationship": "Outer",
				"Parallel Aware": false,
				"Relation Name": "orders",
				"Alias": "orders",
				"Startup Cost": 0.00,
				"Total Cost": 2846.00,
				"Plan Rows": 100000,
				"Plan Width": 72,
				"Actual Startup Time": 0.012,
				"Actual Total Time": 298.123,
				"Actual Rows": 100000,
				"Actual Loops": 1
			}]
		},
		"Planning Time": 0.145,
		"Execution Time": 312.521
	}]`)

	plan, err := parser.Parse(explainJSON)
	if err != nil {
		panic(err)
	}

	adv := advisor.New(
		rules.TopNHeapsort(), // default: flag when child Seq Scan reads >= 1000 rows
	)

	for _, f := range adv.Analyze(plan) {
		fmt.Printf("[%s] %s\n", f.Severity, f.Message)
		fmt.Printf("  detail:     %s\n", f.Detail)
	}

	// Output:
	// [INFO] top-N heapsort on "orders" scanned 100000 rows to return 10
	//   detail:     The query used top-N heapsort to return 10 rows from "orders". This strategy reads every row of the input (100000 rows scanned) and keeps only the top N in a fixed-size heap. It is in-memory and fast, but still performs a full table scan. If a B-tree index exists on (created_at DESC), PostgreSQL could use an Index Scan to read rows in sorted order and stop after the LIMIT — reducing the scan from 100000 rows to just the rows returned.
}

// ExampleParallelNotLaunched demonstrates the ParallelNotLaunched rule using
// the parallel_gather.json fixture: Workers Planned=4 but only 2 launched.
//
// The planner built the plan assuming 4 parallel workers. At runtime only 2
// started — likely because max_parallel_workers or max_parallel_workers_per_gather
// was too low, or other concurrent queries consumed the worker budget.
func ExampleParallelNotLaunched() {
	explainJSON, err := os.ReadFile("../testdata/parallel_gather.json")
	if err != nil {
		panic(err)
	}

	plan, err := parser.Parse(explainJSON)
	if err != nil {
		panic(err)
	}

	adv := advisor.New(
		rules.ParallelNotLaunched(),
	)

	for _, f := range adv.Analyze(plan) {
		node, _ := plan.NodeByID(f.NodeID)
		fmt.Printf("[%s] %s\n", f.Severity, f.Message)
		fmt.Printf("  node:     %s (ID %d)\n", node.NodeType, node.ID)
		fmt.Printf("  planned:  %d\n", *node.WorkersPlanned)
		fmt.Printf("  launched: %d\n", *node.WorkersLaunched)
	}

	// Output:
	// [WARN] Gather launched 2 of 4 planned workers
	//   node:     Gather (ID 1)
	//   planned:  4
	//   launched: 2
}
