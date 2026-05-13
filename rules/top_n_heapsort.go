package rules

import (
	"fmt"
	"strings"

	"github.com/bright98/pgexplain/advisor"
	"github.com/bright98/pgexplain/parser"
)

const defaultMinInputRows = 1000

// TopNHeapsortOption configures the TopNHeapsort rule.
type TopNHeapsortOption func(*topNHeapsortRule)

// WithMinInputRows sets the minimum number of rows the child Seq Scan must
// read before a finding is emitted. This avoids noise on small tables where
// the full scan is cheap regardless.
//
// Default: 1000.
func WithMinInputRows(n int) TopNHeapsortOption {
	return func(r *topNHeapsortRule) {
		r.minInputRows = n
	}
}

// TopNHeapsort returns a Rule that flags Sort nodes that use top-N heapsort
// over a full Seq Scan when an index on the sort key could eliminate the scan.
//
// # What is top-N heapsort?
//
// When a query has ORDER BY ... LIMIT N, PostgreSQL can avoid sorting all rows
// by maintaining a fixed-size heap of the N best rows seen so far. This is the
// "top-N heapsort" strategy: it is always in-memory and faster than a full
// external merge sort.
//
// However, top-N heapsort still reads every row of the input to determine
// which N rows win. If the child node is a Seq Scan on a large table, the
// query reads the entire table just to return a handful of rows.
//
// A B-tree index on the sort key column(s) would allow PostgreSQL to use an
// Index Scan in the correct order and stop after N rows — O(N) instead of
// O(table size).
//
// # What does this rule check?
//
//	IF node is Sort
//	AND Sort Method == "top-N heapsort"
//	AND child node is a Seq Scan
//	AND child rows scanned (ActualRows × ActualLoops) >= minInputRows   (default: 1000)
//	→ emit Info finding
//
// Severity is Info (not Warn) because:
//   - top-N heapsort is in-memory and fast — it is not a confirmed problem
//   - we cannot know from the plan whether an index exists; it may exist but
//     the planner chose not to use it (e.g. stale statistics)
//   - the finding is a hint to investigate, not a definitive diagnosis
//
// # When does this rule fire?
//
// All conditions must hold:
//  1. Node type is Sort.
//  2. Sort Method is "top-N heapsort".
//  3. The child node is a Seq Scan (no index was used at all).
//  4. ANALYZE was run (ActualRows and ActualLoops present on the child).
//  5. child.ActualRows × child.ActualLoops >= minInputRows (default 1000).
//
// # Usage
//
//	rules.TopNHeapsort()
//	rules.TopNHeapsort(rules.WithMinInputRows(10000))
func TopNHeapsort(opts ...TopNHeapsortOption) advisor.Rule {
	r := &topNHeapsortRule{minInputRows: defaultMinInputRows}
	for _, o := range opts {
		o(r)
	}
	return r
}

type topNHeapsortRule struct {
	minInputRows int
}

func (r *topNHeapsortRule) Check(node parser.Node) []advisor.Finding {
	if node.NodeType != parser.NodeSort {
		return nil
	}
	if node.SortMethod == nil || *node.SortMethod != "top-N heapsort" {
		return nil
	}
	if len(node.Plans) == 0 {
		return nil
	}

	child := node.Plans[0]
	if child.NodeType != parser.NodeSeqScan {
		return nil
	}
	if child.ActualRows == nil || child.ActualLoops == nil {
		return nil // ANALYZE was not run
	}

	inputRows := int(*child.ActualRows * *child.ActualLoops)
	if inputRows < r.minInputRows {
		return nil
	}

	relation := "the table"
	if child.RelationName != nil {
		relation = fmt.Sprintf("%q", *child.RelationName)
	}

	returned := ""
	if node.ActualRows != nil {
		returned = fmt.Sprintf(" to return %.0f", *node.ActualRows)
	}

	message := fmt.Sprintf(
		"top-N heapsort on %s scanned %d rows%s",
		relation, inputRows, returned,
	)

	detail := buildTopNDetail(child, node, inputRows)
	suggestion := buildTopNSuggestion(child, node)

	return []advisor.Finding{{
		Severity:   advisor.Info,
		NodeID:     node.ID,
		NodeType:   node.NodeType,
		Message:    message,
		Detail:     detail,
		Suggestion: suggestion,
	}}
}

func buildTopNDetail(child parser.Node, sort parser.Node, inputRows int) string {
	relation := "the table"
	if child.RelationName != nil {
		relation = fmt.Sprintf("%q", *child.RelationName)
	}

	returned := "a small number of rows"
	if sort.ActualRows != nil {
		returned = fmt.Sprintf("%.0f rows", *sort.ActualRows)
	}

	detail := fmt.Sprintf(
		"The query used top-N heapsort to return %s from %s. "+
			"This strategy reads every row of the input (%d rows scanned) "+
			"and keeps only the top N in a fixed-size heap. "+
			"It is in-memory and fast, but still performs a full table scan.",
		returned, relation, inputRows,
	)

	if len(sort.SortKey) > 0 {
		detail += fmt.Sprintf(
			" If a B-tree index exists on (%s), PostgreSQL could use an Index Scan "+
				"to read rows in sorted order and stop after the LIMIT — "+
				"reducing the scan from %d rows to just the rows returned.",
			strings.Join(sort.SortKey, ", "), inputRows,
		)
	}

	return detail
}

func buildTopNSuggestion(child parser.Node, sort parser.Node) string {
	relation := "<table>"
	if child.RelationName != nil {
		relation = *child.RelationName
	}

	if len(sort.SortKey) == 0 {
		return fmt.Sprintf(
			"Consider adding an index on %s that matches the ORDER BY clause. "+
				"Run ANALYZE %s first — the planner may already know about an index "+
				"but chose not to use it due to stale statistics.",
			relation, relation,
		)
	}

	// Build a CREATE INDEX suggestion from the sort key.
	// SortKey entries look like: "col ASC", "col DESC", "col"
	// Strip the direction suffix for the column list in the index name,
	// but keep the full expression (including DESC) in the index definition.
	cols := make([]string, len(sort.SortKey))
	colNames := make([]string, len(sort.SortKey))
	for i, k := range sort.SortKey {
		cols[i] = k
		// strip direction for the index name
		colNames[i] = strings.Fields(k)[0]
	}
	indexName := fmt.Sprintf("%s_%s_idx", relation, strings.Join(colNames, "_"))

	return fmt.Sprintf(
		"First run ANALYZE to ensure statistics are up to date — the planner\n"+
			"may already have an index but chose this plan due to stale row counts:\n"+
			"  ANALYZE %s;\n"+
			"If the plan still shows a Seq Scan after ANALYZE, consider adding\n"+
			"an index on the sort key:\n"+
			"  CREATE INDEX %s ON %s (%s);\n"+
			"After adding the index, re-run EXPLAIN and confirm the Sort node\n"+
			"is replaced by an Index Scan.",
		relation, indexName, relation, strings.Join(cols, ", "),
	)
}
