package rules

import (
	"fmt"

	"github.com/bright98/pgexplain/advisor"
	"github.com/bright98/pgexplain/parser"
)

// SortSpill returns a Rule that warns when a Sort node spills to disk.
//
// # What is a Sort spill?
//
// When PostgreSQL sorts rows (for ORDER BY, GROUP BY, or window functions),
// it allocates memory from work_mem. If the input fits, the sort runs entirely
// in memory using quicksort or top-N heapsort. If the input exceeds work_mem,
// PostgreSQL falls back to an external merge sort: it sorts chunks in memory,
// writes them to temporary disk files, then reads and merges the chunks.
//
// This is indicated by Sort Method "external merge" and Sort Space Type "Disk"
// in EXPLAIN output. Any disk spill means the sort is doing avoidable I/O.
//
// # What does this rule check?
//
//	IF node is a Sort node
//	AND Sort Space Type == "Disk"   (equivalently: Sort Method == "external merge")
//	→ emit Warn finding
//
// There is no configurable threshold — any disk spill is worth flagging.
// The finding includes Sort Space Used (kB) so the caller has a concrete
// work_mem target.
//
// # When does this rule fire?
//
// All conditions must hold:
//  1. Node type is Sort.
//  2. SortSpaceType is non-nil and equals "Disk".
//
// # Usage
//
//	rules.SortSpill()
func SortSpill() advisor.Rule {
	return &sortSpillRule{}
}

type sortSpillRule struct{}

func (r *sortSpillRule) Check(node parser.Node) []advisor.Finding {
	if node.NodeType != parser.NodeSort {
		return nil
	}
	if node.SortSpaceType == nil || *node.SortSpaceType != "Disk" {
		return nil
	}

	method := "external merge"
	if node.SortMethod != nil {
		method = *node.SortMethod
	}

	message := fmt.Sprintf("sort spilled to disk using %s", method)
	if node.SortSpaceUsed != nil {
		message = fmt.Sprintf("sort spilled to disk using %s (%d kB)", method, *node.SortSpaceUsed)
	}

	detail := buildSortSpillDetail(node)
	suggestion := buildSortSpillSuggestion(node)

	return []advisor.Finding{{
		Severity:   advisor.Error,
		NodeID:     node.ID,
		NodeType:   node.NodeType,
		Message:    message,
		Detail:     detail,
		Suggestion: suggestion,
	}}
}

func buildSortSpillDetail(node parser.Node) string {
	base := "The sort could not fit in work_mem and wrote temporary data to disk. " +
		"PostgreSQL sorted chunks in memory, wrote them to temp files, then merged " +
		"the files — reading and writing the sorted data at least twice."

	if node.SortSpaceUsed != nil {
		base += fmt.Sprintf(
			" Disk usage for this sort: %d kB.",
			*node.SortSpaceUsed,
		)
	}

	base += " An in-memory sort (quicksort) is significantly faster because it " +
		"avoids all disk I/O."

	return base
}

func buildSortSpillSuggestion(node parser.Node) string {
	if node.SortSpaceUsed != nil {
		diskKB := *node.SortSpaceUsed
		workMemMB := (diskKB + 1023) / 1024 // round up to next MB
		if workMemMB < 1 {
			workMemMB = 1
		}
		return fmt.Sprintf(
			"Increase work_mem to at least %dMB to allow this sort to run in memory:\n"+
				"  SET work_mem = '%dMB';\n"+
				"For a permanent change, set it per role or in postgresql.conf:\n"+
				"  ALTER ROLE <role> SET work_mem = '%dMB';\n"+
				"Note: work_mem is a per-operation budget. A query with multiple sorts\n"+
				"or hash joins can use it several times simultaneously.",
			workMemMB, workMemMB, workMemMB,
		)
	}
	return "Increase work_mem to allow this sort to run in memory:\n" +
		"  SET work_mem = '<size>';\n" +
		"Re-run EXPLAIN (ANALYZE, FORMAT JSON) and confirm Sort Space Type changes to \"Memory\"."
}
