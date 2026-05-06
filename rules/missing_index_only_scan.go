package rules

import (
	"fmt"
	"math"

	"github.com/Bright98/pgexplain/advisor"
	"github.com/Bright98/pgexplain/parser"
)

const defaultMinHeapFetchRatio = 0.1

// IndexOnlyScanOption configures the MissingIndexOnlyScan rule.
type IndexOnlyScanOption func(*indexOnlyScanRule)

// WithMinHeapFetchRatio sets the minimum fraction of rows that must hit the
// heap before a finding is emitted. The ratio is:
//
//	HeapFetches / (ActualRows × ActualLoops)
//
// Default: 0.1 (10% of rows hitting the heap triggers a Warn).
func WithMinHeapFetchRatio(ratio float64) IndexOnlyScanOption {
	return func(r *indexOnlyScanRule) {
		r.minHeapFetchRatio = ratio
	}
}

// MissingIndexOnlyScan returns a Rule that warns when an Index Only Scan is
// degraded — meaning PostgreSQL is falling back to the heap for a significant
// fraction of rows instead of serving them from the index alone.
//
// # What is an Index Only Scan?
//
// An Index Only Scan is PostgreSQL's most efficient read path: it answers a
// query entirely from the index, never touching the main table (heap). This
// works only when:
//  1. The index covers all columns in the SELECT list and WHERE clause.
//  2. The visibility map marks the page as all-visible, so PostgreSQL can
//     skip the heap check for MVCC visibility.
//
// When condition 2 fails — because VACUUM hasn't run recently enough to mark
// pages as all-visible — PostgreSQL must visit the heap for each row to confirm
// it is visible to the current transaction. These extra heap visits are counted
// in Heap Fetches.
//
// # What does this rule check?
//
//	ratio = HeapFetches / (ActualRows × ActualLoops)
//	if ratio >= minHeapFetchRatio (default 0.1)
//	  → emit Warn finding
//
// A ratio of 0.0 means the Index Only Scan was fully index-covered — ideal.
// A ratio of 1.0 means every single row required a heap visit — equivalent to
// a plain Index Scan with extra overhead.
//
// # When does this rule fire?
//
// All conditions must hold:
//  1. Node type is Index Only Scan.
//  2. HeapFetches is non-nil (present in EXPLAIN output).
//  3. ANALYZE was run (ActualRows and ActualLoops are present).
//  4. ratio >= minHeapFetchRatio.
//
// # Usage
//
//	rules.MissingIndexOnlyScan()
//	rules.MissingIndexOnlyScan(rules.WithMinHeapFetchRatio(0.5))
func MissingIndexOnlyScan(opts ...IndexOnlyScanOption) advisor.Rule {
	r := &indexOnlyScanRule{minHeapFetchRatio: defaultMinHeapFetchRatio}
	for _, o := range opts {
		o(r)
	}
	return r
}

type indexOnlyScanRule struct {
	minHeapFetchRatio float64
}

func (r *indexOnlyScanRule) Check(node parser.Node) []advisor.Finding {
	if node.NodeType != parser.NodeIndexOnlyScan {
		return nil
	}
	if node.HeapFetches == nil {
		return nil
	}
	if node.ActualRows == nil || node.ActualLoops == nil {
		return nil // ANALYZE was not run
	}

	trueActualRows := *node.ActualRows * *node.ActualLoops
	heapFetches := float64(*node.HeapFetches)

	// When zero rows were returned the ratio is undefined; skip to avoid noise.
	if trueActualRows == 0 && heapFetches == 0 {
		return nil
	}

	denominator := math.Max(trueActualRows, 1)
	ratio := heapFetches / denominator

	if ratio < r.minHeapFetchRatio {
		return nil
	}

	pct := int(math.Round(ratio * 100))

	relation := "the index"
	if node.RelationName != nil {
		relation = fmt.Sprintf("%q", *node.RelationName)
	}

	indexPart := ""
	if node.IndexName != nil {
		indexPart = fmt.Sprintf(" (index: %s)", *node.IndexName)
	}

	message := fmt.Sprintf(
		"Index Only Scan on %s%s fetched %d%% of rows from the heap",
		relation, indexPart, pct,
	)

	detail := buildIndexOnlyScanDetail(node, heapFetches, trueActualRows, pct)
	suggestion := buildIndexOnlyScanSuggestion(node)

	return []advisor.Finding{{
		Severity:   advisor.Warn,
		NodeID:     node.ID,
		NodeType:   node.NodeType,
		Message:    message,
		Detail:     detail,
		Suggestion: suggestion,
	}}
}

func buildIndexOnlyScanDetail(node parser.Node, heapFetches, totalRows float64, pct int) string {
	relation := "the table"
	if node.RelationName != nil {
		relation = fmt.Sprintf("%q", *node.RelationName)
	}

	return fmt.Sprintf(
		"An Index Only Scan should serve rows entirely from the index without touching the heap. "+
			"This node returned %.0f rows but fetched %.0f from the heap (%d%%). "+
			"Heap fetches happen when the visibility map does not mark the page as all-visible, "+
			"forcing PostgreSQL to verify row visibility in %s. "+
			"This typically means VACUUM has not run recently enough on the table.",
		totalRows, heapFetches, pct, relation,
	)
}

func buildIndexOnlyScanSuggestion(node parser.Node) string {
	relation := "<table>"
	if node.RelationName != nil {
		relation = *node.RelationName
	}
	return fmt.Sprintf(
		"Run VACUUM on %s to update the visibility map and allow future Index Only Scans "+
			"to skip heap fetches:\n"+
			"  VACUUM %s;\n"+
			"For tables with frequent writes, consider increasing autovacuum frequency:\n"+
			"  ALTER TABLE %s SET (autovacuum_vacuum_scale_factor = 0.01);",
		relation, relation, relation,
	)
}
