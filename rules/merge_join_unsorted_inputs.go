package rules

import (
	"fmt"
	"strings"

	"github.com/bright98/pgexplain/advisor"
	"github.com/bright98/pgexplain/parser"
)

const defaultMinMergeJoinSortRows int64 = 0

// MergeJoinUnsortedInputsOption configures the MergeJoinUnsortedInputs rule.
type MergeJoinUnsortedInputsOption func(*mergeJoinUnsortedInputsRule)

// WithMinMergeJoinSortRows sets the minimum rows (PlanRows of the largest Sort
// child) required to trigger a finding. Use this to silence findings on trivially
// small joins where sorting overhead is negligible.
// Default: 0 (always fire).
func WithMinMergeJoinSortRows(n int64) MergeJoinUnsortedInputsOption {
	return func(r *mergeJoinUnsortedInputsRule) {
		r.minSortRows = n
	}
}

// MergeJoinUnsortedInputs returns a Rule that warns when a Merge Join has
// explicit Sort nodes as direct children, indicating the join key columns lack
// an index that would provide pre-sorted inputs.
//
// # What is a Merge Join with unsorted inputs?
//
// Merge Join reads two pre-sorted streams in lockstep and emits matching pairs.
// It requires both inputs to arrive in sorted order on the join key. When a
// sorted source already exists — typically an Index Scan on the join column —
// PostgreSQL uses it for free. When no sorted source exists, PostgreSQL inserts
// explicit Sort nodes directly above each unsorted input.
//
// Each explicit Sort adds O(N log N) CPU work. If the sorted data exceeds
// work_mem, the sort also spills to disk (see SortSpill), compounding the cost.
//
// # What does this rule check?
//
//	IF node is a Merge Join node
//	AND at least one direct child has Node Type == "Sort"
//	   with Parent Relationship == "Outer" or "Inner"
//	AND max(Sort child PlanRows) >= minSortRows (default 0, always fires)
//	→ emit Warn finding
//
// No ANALYZE is required — the Sort children are visible in any EXPLAIN output.
//
// # When does this rule fire?
//
// All conditions must hold:
//  1. Node type is Merge Join.
//  2. At least one direct child has Node Type == Sort
//     and Parent Relationship of "Outer" or "Inner".
//  3. The largest Sort child's PlanRows >= minSortRows.
//
// # Usage
//
//	rules.MergeJoinUnsortedInputs()
//	rules.MergeJoinUnsortedInputs(rules.WithMinMergeJoinSortRows(1000))
func MergeJoinUnsortedInputs(opts ...MergeJoinUnsortedInputsOption) advisor.Rule {
	r := &mergeJoinUnsortedInputsRule{minSortRows: defaultMinMergeJoinSortRows}
	for _, o := range opts {
		o(r)
	}
	return r
}

type mergeJoinUnsortedInputsRule struct {
	minSortRows int64
}

func (r *mergeJoinUnsortedInputsRule) Check(node parser.Node) []advisor.Finding {
	if node.NodeType != parser.NodeMergeJoin {
		return nil
	}

	outer := findMergeJoinSortChild(node, "Outer")
	inner := findMergeJoinSortChild(node, "Inner")
	if outer == nil && inner == nil {
		return nil
	}

	if r.minSortRows > 0 {
		var maxRows float64
		if outer != nil && outer.PlanRows > maxRows {
			maxRows = outer.PlanRows
		}
		if inner != nil && inner.PlanRows > maxRows {
			maxRows = inner.PlanRows
		}
		if maxRows < float64(r.minSortRows) {
			return nil
		}
	}

	return []advisor.Finding{{
		Severity:   advisor.Warn,
		NodeID:     node.ID,
		NodeType:   node.NodeType,
		Message:    buildMergeJoinMessage(outer, inner),
		Detail:     buildMergeJoinDetail(outer, inner),
		Suggestion: buildMergeJoinSuggestion(outer, inner),
	}}
}

// findMergeJoinSortChild returns the direct child of node that has both
// NodeType == Sort and ParentRelationship == rel, or nil if none exists.
func findMergeJoinSortChild(node parser.Node, rel string) *parser.Node {
	for i := range node.Plans {
		if node.Plans[i].NodeType == parser.NodeSort && node.Plans[i].ParentRelationship == rel {
			return &node.Plans[i]
		}
	}
	return nil
}

func buildMergeJoinMessage(outer, inner *parser.Node) string {
	switch {
	case outer != nil && inner != nil:
		return fmt.Sprintf("merge join sorted both inputs: outer on %s, inner on %s",
			mergeJoinFormatKeys(outer.SortKey), mergeJoinFormatKeys(inner.SortKey))
	case outer != nil:
		return fmt.Sprintf("merge join sorted outer input on %s",
			mergeJoinFormatKeys(outer.SortKey))
	default:
		return fmt.Sprintf("merge join sorted inner input on %s",
			mergeJoinFormatKeys(inner.SortKey))
	}
}

func buildMergeJoinDetail(outer, inner *parser.Node) string {
	detail := "Merge Join requires both inputs to arrive pre-sorted on the join key. " +
		"Without an index scan that produces rows in sorted order, PostgreSQL inserts " +
		"explicit Sort nodes for each unsorted input, adding O(N log N) work before the " +
		"join can begin."

	if outer != nil {
		detail += fmt.Sprintf(" Outer side: %.0f estimated rows sorted on %s",
			outer.PlanRows, mergeJoinFormatKeys(outer.SortKey))
		detail += mergeJoinSpillInfo(outer) + "."
	}
	if inner != nil {
		detail += fmt.Sprintf(" Inner side: %.0f estimated rows sorted on %s",
			inner.PlanRows, mergeJoinFormatKeys(inner.SortKey))
		detail += mergeJoinSpillInfo(inner) + "."
	}

	return detail
}

func mergeJoinSpillInfo(n *parser.Node) string {
	if n.SortSpaceType == nil || *n.SortSpaceType != "Disk" {
		return ""
	}
	if n.SortSpaceUsed != nil {
		return fmt.Sprintf(" (%d kB, spilled to disk)", *n.SortSpaceUsed)
	}
	return " (spilled to disk)"
}

func buildMergeJoinSuggestion(outer, inner *parser.Node) string {
	s := "Create indexes on the join key columns to provide pre-sorted inputs, " +
		"eliminating the Sort overhead:\n"
	if outer != nil && len(outer.SortKey) > 0 {
		s += fmt.Sprintf("  CREATE INDEX ON <outer_table> (%s);\n",
			mergeJoinStripAliases(outer.SortKey))
	}
	if inner != nil && len(inner.SortKey) > 0 {
		s += fmt.Sprintf("  CREATE INDEX ON <inner_table> (%s);\n",
			mergeJoinStripAliases(inner.SortKey))
	}
	s += "After indexing, the planner may choose Index Scans that satisfy the merge " +
		"order without sorting. Re-run EXPLAIN (ANALYZE, FORMAT JSON) to confirm the " +
		"Sort nodes disappear."
	return s
}

func mergeJoinFormatKeys(keys []string) string {
	if len(keys) == 0 {
		return "(unknown)"
	}
	return "(" + strings.Join(keys, ", ") + ")"
}

func mergeJoinStripAliases(keys []string) string {
	stripped := make([]string, len(keys))
	for i, k := range keys {
		if j := strings.LastIndex(k, "."); j >= 0 {
			stripped[i] = k[j+1:]
		} else {
			stripped[i] = k
		}
	}
	return strings.Join(stripped, ", ")
}
