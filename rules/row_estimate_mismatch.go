package rules

import (
	"fmt"
	"math"

	"github.com/pgexplain/pgexplain/advisor"
	"github.com/pgexplain/pgexplain/parser"
)

const (
	defaultMinEstimateFactor float64 = 10.0
	defaultMinRows           float64 = 100.0
)

// RowEstimateMismatchOption configures the RowEstimateMismatch rule.
type RowEstimateMismatchOption func(*rowEstimateMismatchRule)

// WithMinEstimateFactor sets the minimum error factor that triggers a warning.
// The factor is computed as max(planRows, actualRows) / min(planRows, actualRows),
// so it is always >= 1 and symmetric: a 10x underestimate and a 10x overestimate
// are treated the same way.
//
// Default: 10.
func WithMinEstimateFactor(factor float64) RowEstimateMismatchOption {
	return func(r *rowEstimateMismatchRule) {
		r.minEstimateFactor = factor
	}
}

// WithMinRows sets the minimum row count floor. The rule is skipped when the
// larger of planRows and trueActualRows is below this value. This prevents
// noise from tiny nodes where a 10x error on 3 rows causes no real harm.
//
// Default: 100.
func WithMinRows(rows float64) RowEstimateMismatchOption {
	return func(r *rowEstimateMismatchRule) {
		r.minRows = rows
	}
}

// RowEstimateMismatch returns a Rule that warns when the planner's row estimate
// diverges significantly from the actual row count produced at runtime.
//
// # What is a row estimate mismatch?
//
// PostgreSQL's planner estimates how many rows each node will produce, using
// statistics collected by ANALYZE. These estimates drive every planning decision:
// join strategy, join order, work_mem allocation, and index vs seq scan choice.
// When the estimate is wildly wrong, those decisions may be wrong too.
//
// The rule fires on any node type — not just leaf scans — because mismatch at
// intermediate nodes (Hash Join, Sort, Aggregate) is equally harmful.
//
// A critical detail: EXPLAIN reports Actual Rows per loop execution. Inside a
// Nested Loop, a node may execute thousands of times. The true total actual rows
// is Actual Rows × Actual Loops, and that is what this rule compares against
// Plan Rows.
//
// # When does this rule fire?
//
// All three conditions must hold:
//  1. EXPLAIN was run with ANALYZE (Actual Rows and Actual Loops are present).
//  2. max(planRows, trueActualRows) >= minRows (default 100) — avoids noise on tiny nodes.
//  3. max(planRows, trueActualRows) / min(planRows, trueActualRows) >= minEstimateFactor (default 10).
//
// # Usage
//
//	rules.RowEstimateMismatch()
//	rules.RowEstimateMismatch(rules.WithMinEstimateFactor(50))
//	rules.RowEstimateMismatch(rules.WithMinRows(500))
func RowEstimateMismatch(opts ...RowEstimateMismatchOption) advisor.Rule {
	r := &rowEstimateMismatchRule{
		minEstimateFactor: defaultMinEstimateFactor,
		minRows:           defaultMinRows,
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

type rowEstimateMismatchRule struct {
	minEstimateFactor float64
	minRows           float64
}

func (r *rowEstimateMismatchRule) Check(node parser.Node) []advisor.Finding {
	// Actual Rows and Actual Loops are only present when EXPLAIN ANALYZE was used.
	if node.ActualRows == nil || node.ActualLoops == nil {
		return nil
	}

	planRows := node.PlanRows
	trueActualRows := *node.ActualRows * *node.ActualLoops

	// Skip when both sides are below the row floor — a 10x error on 3 rows is harmless.
	if math.Max(planRows, trueActualRows) < r.minRows {
		return nil
	}

	// Compute the error factor symmetrically. Use 1 as the denominator floor to
	// avoid division by zero when one side is 0 (e.g. Plan Rows: 0 in CTEs).
	larger := math.Max(planRows, trueActualRows)
	smaller := math.Min(planRows, trueActualRows)
	if smaller == 0 {
		smaller = 1
	}
	factor := larger / smaller

	if factor < r.minEstimateFactor {
		return nil
	}

	direction := "underestimate"
	if planRows > trueActualRows {
		direction = "overestimate"
	}

	return []advisor.Finding{{
		Severity: advisor.Warn,
		NodeID:   node.ID,
		NodeType: node.NodeType,
		Message: fmt.Sprintf(
			"row estimate for %s was off by %.0fx (%s: planned %.0f, got %.0f)",
			node.NodeType, factor, direction, planRows, trueActualRows,
		),
		Detail: fmt.Sprintf(
			"The planner estimated %.0f rows but this node produced %.0f (loops=%.0f). "+
				"A %.0fx %s can cause the planner to choose the wrong join strategy, "+
				"misallocate work_mem, or produce a suboptimal join order.",
			planRows, trueActualRows, *node.ActualLoops, factor, direction,
		),
		Suggestion: "Run ANALYZE on the tables involved in this node to refresh planner statistics. " +
			"If the mismatch persists, consider raising the statistics target for the relevant columns: " +
			"ALTER TABLE <table> ALTER COLUMN <column> SET STATISTICS 500;",
	}}
}
