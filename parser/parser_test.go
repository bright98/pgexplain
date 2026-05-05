package parser

import (
	"os"
	"path/filepath"
	"testing"
)

func ptr[T any](v T) *T { return &v }

func mustReadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "testdata", name))
	if err != nil {
		t.Fatalf("reading fixture %q: %v", name, err)
	}
	return data
}

func TestParse_SeqScan(t *testing.T) {
	plan, err := Parse(mustReadFixture(t, "seq_scan.json"))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	// Top-level timing
	if got, want := plan.PlanningTime, 0.123; got != want {
		t.Errorf("PlanningTime = %v, want %v", got, want)
	}
	if got, want := plan.ExecutionTime, 18.854; got != want {
		t.Errorf("ExecutionTime = %v, want %v", got, want)
	}

	root := plan.Node

	// Node type and relation
	if got, want := root.NodeType, "Seq Scan"; got != want {
		t.Errorf("NodeType = %q, want %q", got, want)
	}
	if root.RelationName == nil || *root.RelationName != "orders" {
		t.Errorf("RelationName = %v, want %q", root.RelationName, "orders")
	}
	if root.Alias == nil || *root.Alias != "orders" {
		t.Errorf("Alias = %v, want %q", root.Alias, "orders")
	}

	// Planner estimates
	if got, want := root.StartupCost, 0.00; got != want {
		t.Errorf("StartupCost = %v, want %v", got, want)
	}
	if got, want := root.TotalCost, 1849.00; got != want {
		t.Errorf("TotalCost = %v, want %v", got, want)
	}
	if got, want := root.PlanRows, float64(15); got != want {
		t.Errorf("PlanRows = %v, want %v", got, want)
	}

	// Actuals
	if root.ActualTotalTime == nil {
		t.Fatal("ActualTotalTime is nil, want non-nil (ANALYZE was run)")
	}
	if got, want := *root.ActualTotalTime, 18.721; got != want {
		t.Errorf("ActualTotalTime = %v, want %v", got, want)
	}
	if root.ActualRows == nil || *root.ActualRows != 12 {
		t.Errorf("ActualRows = %v, want 12", root.ActualRows)
	}

	// Filter
	if root.Filter == nil || *root.Filter != "(customer_id = 42)" {
		t.Errorf("Filter = %v, want %q", root.Filter, "(customer_id = 42)")
	}
	if root.RowsRemovedByFilter == nil || *root.RowsRemovedByFilter != 99988 {
		t.Errorf("RowsRemovedByFilter = %v, want 99988", root.RowsRemovedByFilter)
	}

	// Block I/O
	if root.SharedHitBlocks == nil || *root.SharedHitBlocks != 549 {
		t.Errorf("SharedHitBlocks = %v, want 549", root.SharedHitBlocks)
	}

	// No children
	if got := len(root.Plans); got != 0 {
		t.Errorf("len(Plans) = %d, want 0", got)
	}

	// Fields absent for this node type must be nil
	if root.HashCond != nil {
		t.Errorf("HashCond = %v, want nil", root.HashCond)
	}
	if root.SortKey != nil {
		t.Errorf("SortKey = %v, want nil", root.SortKey)
	}
	if root.IndexName != nil {
		t.Errorf("IndexName = %v, want nil", root.IndexName)
	}
}

func TestParse_HashJoinSort(t *testing.T) {
	plan, err := Parse(mustReadFixture(t, "hash_join_sort.json"))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	root := plan.Node

	// Root is Sort
	if got, want := root.NodeType, "Sort"; got != want {
		t.Errorf("root NodeType = %q, want %q", got, want)
	}
	if root.SortKey == nil || len(root.SortKey) != 1 || root.SortKey[0] != "o.created_at" {
		t.Errorf("SortKey = %v, want [o.created_at]", root.SortKey)
	}
	if root.SortMethod == nil || *root.SortMethod != "quicksort" {
		t.Errorf("SortMethod = %v, want %q", root.SortMethod, "quicksort")
	}
	if root.SortSpaceUsed == nil || *root.SortSpaceUsed != 412 {
		t.Errorf("SortSpaceUsed = %v, want 412", root.SortSpaceUsed)
	}
	if root.SortSpaceType == nil || *root.SortSpaceType != "Memory" {
		t.Errorf("SortSpaceType = %v, want %q", root.SortSpaceType, "Memory")
	}

	// Sort has one child: Hash Join
	if got := len(root.Plans); got != 1 {
		t.Fatalf("root len(Plans) = %d, want 1", got)
	}
	hashJoin := root.Plans[0]
	if got, want := hashJoin.NodeType, "Hash Join"; got != want {
		t.Errorf("child NodeType = %q, want %q", got, want)
	}
	if got, want := hashJoin.ParentRelationship, "Outer"; got != want {
		t.Errorf("ParentRelationship = %q, want %q", got, want)
	}
	if hashJoin.HashCond == nil || *hashJoin.HashCond != "(o.customer_id = c.id)" {
		t.Errorf("HashCond = %v, want %q", hashJoin.HashCond, "(o.customer_id = c.id)")
	}

	// Hash Join has two children: Seq Scan (orders) and Hash
	if got := len(hashJoin.Plans); got != 2 {
		t.Fatalf("hashJoin len(Plans) = %d, want 2", got)
	}
	seqScanOrders := hashJoin.Plans[0]
	if got, want := seqScanOrders.NodeType, "Seq Scan"; got != want {
		t.Errorf("orders NodeType = %q, want %q", got, want)
	}
	if seqScanOrders.RelationName == nil || *seqScanOrders.RelationName != "orders" {
		t.Errorf("orders RelationName = %v, want %q", seqScanOrders.RelationName, "orders")
	}

	hashNode := hashJoin.Plans[1]
	if got, want := hashNode.NodeType, "Hash"; got != want {
		t.Errorf("hash NodeType = %q, want %q", got, want)
	}
	if hashNode.HashBuckets == nil || *hashNode.HashBuckets != 1024 {
		t.Errorf("HashBuckets = %v, want 1024", hashNode.HashBuckets)
	}
	if hashNode.HashBatches == nil || *hashNode.HashBatches != 1 {
		t.Errorf("HashBatches = %v, want 1", hashNode.HashBatches)
	}
	if hashNode.PeakMemoryUsage == nil || *hashNode.PeakMemoryUsage != 32 {
		t.Errorf("PeakMemoryUsage = %v, want 32", hashNode.PeakMemoryUsage)
	}

	// Hash's child: Seq Scan (customers) with filter
	if got := len(hashNode.Plans); got != 1 {
		t.Fatalf("hashNode len(Plans) = %d, want 1", got)
	}
	seqScanCustomers := hashNode.Plans[0]
	if seqScanCustomers.RelationName == nil || *seqScanCustomers.RelationName != "customers" {
		t.Errorf("customers RelationName = %v, want %q", seqScanCustomers.RelationName, "customers")
	}
	if seqScanCustomers.Filter == nil || *seqScanCustomers.Filter != "(region = 'EU')" {
		t.Errorf("customers Filter = %v, want %q", seqScanCustomers.Filter, "(region = 'EU')")
	}
	if seqScanCustomers.RowsRemovedByFilter == nil || *seqScanCustomers.RowsRemovedByFilter != 977 {
		t.Errorf("RowsRemovedByFilter = %v, want 977", seqScanCustomers.RowsRemovedByFilter)
	}
}

func TestParse_IndexScan(t *testing.T) {
	plan, err := Parse(mustReadFixture(t, "index_scan.json"))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	root := plan.Node
	if got, want := root.NodeType, "Index Scan"; got != want {
		t.Errorf("NodeType = %q, want %q", got, want)
	}
	if root.IndexName == nil || *root.IndexName != "orders_customer_id_idx" {
		t.Errorf("IndexName = %v, want %q", root.IndexName, "orders_customer_id_idx")
	}
	if root.IndexCond == nil || *root.IndexCond != "(customer_id = 42)" {
		t.Errorf("IndexCond = %v, want %q", root.IndexCond, "(customer_id = 42)")
	}

	// No filter (index handles the predicate exactly)
	if root.Filter != nil {
		t.Errorf("Filter = %v, want nil", root.Filter)
	}
	if root.RowsRemovedByFilter != nil {
		t.Errorf("RowsRemovedByFilter = %v, want nil", root.RowsRemovedByFilter)
	}

	// Planner estimated 3 rows, got 3 — good estimate
	if got, want := root.PlanRows, float64(3); got != want {
		t.Errorf("PlanRows = %v, want %v", got, want)
	}
	if root.ActualRows == nil || *root.ActualRows != 3 {
		t.Errorf("ActualRows = %v, want 3", root.ActualRows)
	}

	// Block I/O
	if root.SharedHitBlocks == nil || *root.SharedHitBlocks != 4 {
		t.Errorf("SharedHitBlocks = %v, want 4", root.SharedHitBlocks)
	}
}

func TestParse_Errors(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "invalid JSON",
			input: `not json at all`,
		},
		{
			name:  "empty array",
			input: `[]`,
		},
		{
			name:  "empty input",
			input: ``,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := Parse([]byte(tt.input))
			if err == nil {
				t.Errorf("Parse(%q) = %+v, want error", tt.input, plan)
			}
		})
	}
}

func TestParse_NodeIDs_Sequential(t *testing.T) {
	// hash_join_sort.json has 5 nodes in pre-order:
	// 1=Sort, 2=Hash Join, 3=Seq Scan(orders), 4=Hash, 5=Seq Scan(customers)
	plan, err := Parse(mustReadFixture(t, "hash_join_sort.json"))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	tests := []struct {
		path     string
		wantID   int
		wantType string
	}{
		{"root", plan.Node.ID, NodeSort},
		{"root.Plans[0]", plan.Node.Plans[0].ID, NodeHashJoin},
		{"root.Plans[0].Plans[0]", plan.Node.Plans[0].Plans[0].ID, NodeSeqScan},
		{"root.Plans[0].Plans[1]", plan.Node.Plans[0].Plans[1].ID, NodeHash},
		{"root.Plans[0].Plans[1].Plans[0]", plan.Node.Plans[0].Plans[1].Plans[0].ID, NodeSeqScan},
	}

	for i, tt := range tests {
		wantID := i + 1
		if tt.wantID != wantID {
			t.Errorf("%s: ID = %d, want %d", tt.path, tt.wantID, wantID)
		}
		// NodeType validated indirectly via the path — just make sure IDs are unique
	}
}

func TestParse_NodeByID(t *testing.T) {
	plan, err := Parse(mustReadFixture(t, "hash_join_sort.json"))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	tests := []struct {
		id       int
		wantType string
		wantOK   bool
	}{
		{1, NodeSort, true},
		{2, NodeHashJoin, true},
		{3, NodeSeqScan, true},
		{4, NodeHash, true},
		{5, NodeSeqScan, true},
		{0, "", false},   // 0 is never assigned
		{99, "", false},  // out of range
	}

	for _, tt := range tests {
		node, ok := plan.NodeByID(tt.id)
		if ok != tt.wantOK {
			t.Errorf("NodeByID(%d) ok = %v, want %v", tt.id, ok, tt.wantOK)
			continue
		}
		if ok && node.NodeType != tt.wantType {
			t.Errorf("NodeByID(%d).NodeType = %q, want %q", tt.id, node.NodeType, tt.wantType)
		}
	}
}

func TestParse_NestedLoop(t *testing.T) {
	plan, err := Parse(mustReadFixture(t, "nested_loop.json"))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	root := plan.Node
	if got, want := root.NodeType, NodeNestedLoop; got != want {
		t.Errorf("root NodeType = %q, want %q", got, want)
	}
	if got := len(root.Plans); got != 2 {
		t.Fatalf("root len(Plans) = %d, want 2", got)
	}

	outer := root.Plans[0]
	if got, want := outer.NodeType, NodeSeqScan; got != want {
		t.Errorf("outer NodeType = %q, want %q", got, want)
	}
	if got, want := outer.ParentRelationship, "Outer"; got != want {
		t.Errorf("outer ParentRelationship = %q, want %q", got, want)
	}
	if outer.ActualRows == nil || *outer.ActualRows != 1000 {
		t.Errorf("outer ActualRows = %v, want 1000", outer.ActualRows)
	}
	// Outer always executes once at the top level
	if outer.ActualLoops == nil || *outer.ActualLoops != 1 {
		t.Errorf("outer ActualLoops = %v, want 1", outer.ActualLoops)
	}

	inner := root.Plans[1]
	if got, want := inner.NodeType, NodeIndexScan; got != want {
		t.Errorf("inner NodeType = %q, want %q", got, want)
	}
	if got, want := inner.ParentRelationship, "Inner"; got != want {
		t.Errorf("inner ParentRelationship = %q, want %q", got, want)
	}
	if inner.RelationName == nil || *inner.RelationName != "order_items" {
		t.Errorf("inner RelationName = %v, want %q", inner.RelationName, "order_items")
	}
	// The critical field: inner executes once per outer row → 1000 loops
	if inner.ActualLoops == nil || *inner.ActualLoops != 1000 {
		t.Errorf("inner ActualLoops = %v, want 1000", inner.ActualLoops)
	}
	if inner.ActualRows == nil || *inner.ActualRows != 3 {
		t.Errorf("inner ActualRows (per loop) = %v, want 3", inner.ActualRows)
	}
	if inner.IndexCond == nil || *inner.IndexCond != "(order_id = o.id)" {
		t.Errorf("inner IndexCond = %v, want %q", inner.IndexCond, "(order_id = o.id)")
	}
}

func TestParse_IndexOnlyScan(t *testing.T) {
	plan, err := Parse(mustReadFixture(t, "index_only_scan.json"))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	root := plan.Node
	if got, want := root.NodeType, NodeIndexOnlyScan; got != want {
		t.Errorf("NodeType = %q, want %q", got, want)
	}
	if root.IndexName == nil || *root.IndexName != "orders_customer_id_created_at_idx" {
		t.Errorf("IndexName = %v, want %q", root.IndexName, "orders_customer_id_created_at_idx")
	}
	if root.IndexCond == nil || *root.IndexCond != "(customer_id = 42)" {
		t.Errorf("IndexCond = %v, want %q", root.IndexCond, "(customer_id = 42)")
	}
	// HeapFetches = 0: all rows served from the index, no heap access needed
	if root.HeapFetches == nil {
		t.Fatal("HeapFetches is nil, want 0")
	}
	if *root.HeapFetches != 0 {
		t.Errorf("HeapFetches = %d, want 0", *root.HeapFetches)
	}
	// No filter: the index covers the predicate exactly
	if root.Filter != nil {
		t.Errorf("Filter = %v, want nil", root.Filter)
	}
	if root.SharedHitBlocks == nil || *root.SharedHitBlocks != 3 {
		t.Errorf("SharedHitBlocks = %v, want 3", root.SharedHitBlocks)
	}
}

func TestParse_SortSpill(t *testing.T) {
	plan, err := Parse(mustReadFixture(t, "sort_spill.json"))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	root := plan.Node
	if got, want := root.NodeType, NodeSort; got != want {
		t.Errorf("NodeType = %q, want %q", got, want)
	}
	// "external merge" is the sort method PostgreSQL uses when spilling to disk
	if root.SortMethod == nil || *root.SortMethod != "external merge" {
		t.Errorf("SortMethod = %v, want %q", root.SortMethod, "external merge")
	}
	// "Disk" distinguishes a spill from an in-memory sort
	if root.SortSpaceType == nil || *root.SortSpaceType != "Disk" {
		t.Errorf("SortSpaceType = %v, want %q", root.SortSpaceType, "Disk")
	}
	if root.SortSpaceUsed == nil || *root.SortSpaceUsed != 18432 {
		t.Errorf("SortSpaceUsed = %v, want 18432", root.SortSpaceUsed)
	}
	if root.SortKey == nil || len(root.SortKey) != 1 || root.SortKey[0] != "created_at DESC" {
		t.Errorf("SortKey = %v, want [created_at DESC]", root.SortKey)
	}
	if got := len(root.Plans); got != 1 {
		t.Fatalf("len(Plans) = %d, want 1", got)
	}
}

func TestParse_ParallelGather(t *testing.T) {
	plan, err := Parse(mustReadFixture(t, "parallel_gather.json"))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	root := plan.Node
	if got, want := root.NodeType, NodeGather; got != want {
		t.Errorf("NodeType = %q, want %q", got, want)
	}
	if root.WorkersPlanned == nil || *root.WorkersPlanned != 4 {
		t.Errorf("WorkersPlanned = %v, want 4", root.WorkersPlanned)
	}
	// Workers Launched < Workers Planned: the signal for ParallelNotLaunched rule
	if root.WorkersLaunched == nil || *root.WorkersLaunched != 2 {
		t.Errorf("WorkersLaunched = %v, want 2", root.WorkersLaunched)
	}
	if got := len(root.Plans); got != 1 {
		t.Fatalf("len(Plans) = %d, want 1", got)
	}

	child := root.Plans[0]
	if got, want := child.NodeType, NodeSeqScan; got != want {
		t.Errorf("child NodeType = %q, want %q", got, want)
	}
	// Parallel Aware = true: this node runs inside a parallel worker
	if !child.ParallelAware {
		t.Error("child ParallelAware = false, want true")
	}
	// ActualLoops = 3: leader process + 2 launched workers each executed this node
	if child.ActualLoops == nil || *child.ActualLoops != 3 {
		t.Errorf("child ActualLoops = %v, want 3", child.ActualLoops)
	}
}

func TestParse_NodeID_RootIsAlwaysOne(t *testing.T) {
	fixtures := []string{"seq_scan.json", "hash_join_sort.json", "index_scan.json",
		"nested_loop.json", "index_only_scan.json", "sort_spill.json", "parallel_gather.json"}
	for _, f := range fixtures {
		plan, err := Parse(mustReadFixture(t, f))
		if err != nil {
			t.Fatalf("%s: Parse() error = %v", f, err)
		}
		if plan.Node.ID != 1 {
			t.Errorf("%s: root ID = %d, want 1", f, plan.Node.ID)
		}
	}
}

func TestParse_WithoutAnalyze(t *testing.T) {
	// EXPLAIN without ANALYZE: no Actual* fields, no Execution Time
	input := `[
		{
			"Plan": {
				"Node Type": "Seq Scan",
				"Parallel Aware": false,
				"Relation Name": "orders",
				"Alias": "orders",
				"Startup Cost": 0.00,
				"Total Cost": 1849.00,
				"Plan Rows": 100000,
				"Plan Width": 72
			},
			"Planning Time": 0.089
		}
	]`

	plan, err := Parse([]byte(input))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	root := plan.Node
	if root.ActualStartupTime != nil {
		t.Errorf("ActualStartupTime = %v, want nil (no ANALYZE)", root.ActualStartupTime)
	}
	if root.ActualTotalTime != nil {
		t.Errorf("ActualTotalTime = %v, want nil (no ANALYZE)", root.ActualTotalTime)
	}
	if root.ActualRows != nil {
		t.Errorf("ActualRows = %v, want nil (no ANALYZE)", root.ActualRows)
	}
	if got, want := plan.ExecutionTime, 0.0; got != want {
		t.Errorf("ExecutionTime = %v, want %v (no ANALYZE)", got, want)
	}
	if got, want := plan.PlanningTime, 0.089; got != want {
		t.Errorf("PlanningTime = %v, want %v", got, want)
	}
}
