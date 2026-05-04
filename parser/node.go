// Package parser parses PostgreSQL EXPLAIN (ANALYZE, FORMAT JSON) output
// into a typed Go plan tree.
package parser

// Node type constants match the "Node Type" field in PostgreSQL's EXPLAIN JSON.
// These values are fixed by PostgreSQL's source code and stable across versions.
// New node types may be added in future PostgreSQL releases but existing ones are never renamed.
const (
	NodeSeqScan       = "Seq Scan"
	NodeIndexScan     = "Index Scan"
	NodeIndexOnlyScan = "Index Only Scan"
	NodeBitmapScan    = "Bitmap Heap Scan"
	NodeHashJoin      = "Hash Join"
	NodeMergeJoin     = "Merge Join"
	NodeNestedLoop    = "Nested Loop"
	NodeHash          = "Hash"
	NodeSort          = "Sort"
	NodeAggregate     = "Aggregate"
	NodeGather        = "Gather"
	NodeGatherMerge   = "Gather Merge"
	NodeLimit         = "Limit"
	NodeResult        = "Result"
	NodeAppend        = "Append"
	NodeSubqueryScan  = "Subquery Scan"
	NodeCTEScan       = "CTE Scan"
)

// Plan is the top-level result of parsing EXPLAIN JSON output.
// PostgreSQL always wraps the plan in a single-element JSON array.
type Plan struct {
	Node          Node
	PlanningTime  float64 // ms; always present
	ExecutionTime float64 // ms; zero if EXPLAIN was run without ANALYZE
}

// Node represents one node in the plan tree. Fields that are absent for
// certain node types use pointer types — nil means the field was not present
// in the JSON, not that its value is zero.
type Node struct {
	// --- Identity ---
	ID                 int    // assigned by Parse() in depth-first pre-order starting at 1; not from JSON
	NodeType           string `json:"Node Type"`
	ParentRelationship string `json:"Parent Relationship"`

	// --- Planner estimates (always present) ---
	StartupCost float64 `json:"Startup Cost"`
	TotalCost   float64 `json:"Total Cost"`
	PlanRows    float64 `json:"Plan Rows"` // float64: JSON can emit fractional values
	PlanWidth   int     `json:"Plan Width"`

	// --- Actuals (present only with ANALYZE) ---
	ActualStartupTime *float64 `json:"Actual Startup Time"`
	ActualTotalTime   *float64 `json:"Actual Total Time"`
	ActualRows        *float64 `json:"Actual Rows"`
	ActualLoops       *float64 `json:"Actual Loops"`

	// --- Relation info (Seq Scan, Index Scan, Index Only Scan, Bitmap Heap Scan) ---
	RelationName *string `json:"Relation Name"`
	Alias        *string `json:"Alias"`
	IndexName    *string `json:"Index Name"`

	// --- Filter and join conditions ---
	Filter              *string `json:"Filter"`
	RowsRemovedByFilter *int64  `json:"Rows Removed by Filter"`
	JoinFilter          *string `json:"Join Filter"`
	HashCond            *string `json:"Hash Cond"`
	IndexCond           *string `json:"Index Cond"`
	RecheckCond         *string `json:"Recheck Cond"`

	// --- Sort ---
	SortKey       []string `json:"Sort Key"`
	SortMethod    *string  `json:"Sort Method"`
	SortSpaceUsed *int64   `json:"Sort Space Used"` // kB
	SortSpaceType *string  `json:"Sort Space Type"` // "Memory" or "Disk"

	// --- Hash ---
	HashBuckets     *int   `json:"Hash Buckets"`
	HashBatches     *int   `json:"Hash Batches"`
	PeakMemoryUsage *int64 `json:"Peak Memory Usage"` // kB

	// --- Parallel ---
	ParallelAware   bool `json:"Parallel Aware"`
	WorkersPlanned  *int `json:"Workers Planned"`
	WorkersLaunched *int `json:"Workers Launched"`

	// --- Block I/O (present only with BUFFERS option) ---
	SharedHitBlocks     *int64 `json:"Shared Hit Blocks"`
	SharedReadBlocks    *int64 `json:"Shared Read Blocks"`
	SharedDirtiedBlocks *int64 `json:"Shared Dirtied Blocks"`
	SharedWrittenBlocks *int64 `json:"Shared Written Blocks"`
	TempReadBlocks      *int64 `json:"Temp Read Blocks"`
	TempWrittenBlocks   *int64 `json:"Temp Written Blocks"`

	// --- Recursive children ---
	Plans []Node `json:"Plans"`
}
