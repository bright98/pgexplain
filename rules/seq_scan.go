package rules

import (
	"fmt"

	"github.com/pgexplain/pgexplain/advisor"
	"github.com/pgexplain/pgexplain/parser"
)

const (
	defaultMinFilterRatio float64 = 10.0
	defaultMinRowsRemoved int64   = 1000
)

// SeqScanOption configures the SeqScan rule.
type SeqScanOption func(*seqScanRule)

// WithMinFilterRatio sets the minimum ratio of rows discarded to rows returned
// that triggers a warning. A ratio of 10 means the scan throws away at least
// 10 rows for every 1 row it returns.
//
// Lower values catch more marginal cases; higher values reduce noise.
// Default: 10.
func WithMinFilterRatio(ratio float64) SeqScanOption {
	return func(r *seqScanRule) {
		r.minFilterRatio = ratio
	}
}

// SeqScan returns a Rule that warns when a sequential scan filters out many
// more rows than it returns — a strong signal that an index on the filtered
// column(s) would significantly reduce I/O.
//
// # What is a Sequential Scan?
//
// A Seq Scan reads every row in the table in physical order and evaluates the
// WHERE clause on each one. It is the simplest access method and is correct
// for small tables or queries that need most of the table. But when a filter
// rejects the vast majority of rows, the scan does far more work than
// necessary — work that a B-tree index would skip entirely.
//
// # When does this rule fire?
//
// The rule fires when all three conditions hold:
//  1. The node is a Seq Scan with a Filter condition (no filter = intentional full read).
//  2. rows_removed_by_filter / actual_rows >= minFilterRatio (default 10).
//  3. Special case: if actual_rows is 0 (nothing matched), fires when
//     rows_removed_by_filter >= 1000, since division is undefined.
//
// # Usage
//
//	rules.SeqScan()                            // default: ratio >= 10
//	rules.SeqScan(rules.WithMinFilterRatio(5)) // stricter: ratio >= 5
func SeqScan(opts ...SeqScanOption) advisor.Rule {
	r := &seqScanRule{
		minFilterRatio: defaultMinFilterRatio,
		minRowsRemoved: defaultMinRowsRemoved,
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

type seqScanRule struct {
	minFilterRatio float64
	minRowsRemoved int64
}

func (r *seqScanRule) Check(node parser.Node) []advisor.Finding {
	if node.NodeType != parser.NodeSeqScan {
		return nil
	}
	if node.RowsRemovedByFilter == nil {
		// No filter means this is a deliberate full read (e.g. SELECT * FROM t).
		// The planner chose Seq Scan intentionally; nothing to flag.
		return nil
	}

	removed := *node.RowsRemovedByFilter
	var actualRows float64
	if node.ActualRows != nil {
		actualRows = *node.ActualRows
	}

	wasteful := false
	if actualRows == 0 {
		wasteful = removed >= r.minRowsRemoved
	} else {
		wasteful = float64(removed)/actualRows >= r.minFilterRatio
	}
	if !wasteful {
		return nil
	}

	relation := "unknown"
	if node.RelationName != nil {
		relation = *node.RelationName
	}
	filter := filterClause(node)

	var message, detail string
	if actualRows == 0 {
		message = fmt.Sprintf(
			"sequential scan on %q removed %d rows, returned none",
			relation, removed,
		)
		detail = fmt.Sprintf(
			"PostgreSQL read and discarded %d rows from %q while evaluating %s. "+
				"No rows matched. An index on the filtered column(s) would avoid this full scan.",
			removed, relation, filter,
		)
	} else {
		ratio := float64(removed) / actualRows
		message = fmt.Sprintf(
			"sequential scan on %q discards %.0fx more rows than it returns",
			relation, ratio,
		)
		detail = fmt.Sprintf(
			"PostgreSQL read %d rows from %q but only %d matched %s "+
				"(%.0f rows discarded per row returned). "+
				"An index on the filtered column(s) would allow PostgreSQL to skip non-matching rows.",
			int64(actualRows)+removed, relation, int64(actualRows), filter, ratio,
		)
	}

	return []advisor.Finding{{
		Severity: advisor.Warn,
		NodeID:   node.ID,
		NodeType: node.NodeType,
		Message:  message,
		Detail:   detail,
		Suggestion: fmt.Sprintf(
			"Add an index on %q to support the filter %s. "+
				"Run EXPLAIN (ANALYZE, BUFFERS) after adding the index to confirm it is used.",
			relation, filter,
		),
	}}
}

// filterClause returns the filter string for display, or a fallback if absent.
func filterClause(node parser.Node) string {
	if node.Filter != nil {
		return *node.Filter
	}
	return "(unknown filter)"
}
