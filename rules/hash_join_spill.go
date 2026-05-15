package rules

import (
	"fmt"

	"github.com/bright98/pgexplain/advisor"
	"github.com/bright98/pgexplain/parser"
)

// HashJoinSpill returns a Rule that warns when a Hash Join spills its hash
// table to disk because the inner relation exceeded work_mem.
//
// # What is a hash join spill?
//
// A Hash Join builds a hash table from the inner (smaller) relation in memory,
// then probes it with each row from the outer relation. The memory budget for
// this hash table comes from work_mem (default: 4MB).
//
// When the inner relation is too large to fit in work_mem, PostgreSQL falls
// back to a batched strategy: it splits both relations into N batches, writes
// them to temporary disk files, and processes one batch at a time. This is
// indicated by Hash Batches > 1 on the Hash node.
//
// Disk-based batching is orders of magnitude slower than an in-memory hash
// join because each row may be written to and read from disk multiple times.
//
// # What does this rule check?
//
// The rule fires on the Hash node (the inner child of a Hash Join) when:
//   - Hash Batches > 1 (any spill, regardless of batch count)
//
// There is no configurable threshold — a spill is always worth flagging.
// The finding includes Peak Memory Usage to give a concrete work_mem suggestion.
//
// # Future work
//
// PostgreSQL also reports Original Hash Batches (the initially planned batch
// count) separately from the final Hash Batches. A plan that started with
// 1 batch but grew to 4 during execution indicates a particularly bad estimate.
// This distinction is not yet captured in the parser's Node struct.
//
// # Usage
//
//	rules.HashJoinSpill()
func HashJoinSpill() advisor.Rule {
	return hashJoinSpillRule{}
}

type hashJoinSpillRule struct{}

func (hashJoinSpillRule) Check(node parser.Node) []advisor.Finding {
	if node.NodeType != parser.NodeHash {
		return nil
	}
	if node.HashBatches == nil || *node.HashBatches <= 1 {
		return nil
	}

	batches := *node.HashBatches
	message := fmt.Sprintf(
		"hash join spilled to disk across %d batches",
		batches,
	)
	if node.PeakMemoryUsage != nil {
		message = fmt.Sprintf(
			"hash join spilled to disk across %d batches (peak memory: %dkB per batch)",
			batches, *node.PeakMemoryUsage,
		)
	}

	detail := fmt.Sprintf(
		"The hash table for this join exceeded work_mem and was split into %d batches. "+
			"Each batch was written to and read from temporary disk files. "+
			"Disk-based batching is orders of magnitude slower than an in-memory hash join.",
		batches,
	)
	if node.PeakMemoryUsage != nil {
		totalKB := int64(batches) * *node.PeakMemoryUsage
		detail = fmt.Sprintf(
			"The hash table for this join exceeded work_mem and was split into %d batches. "+
				"Each batch was written to and read from temporary disk files. "+
				"The join processed approximately %dkB of hash table data across all batches. "+
				"Disk-based batching is orders of magnitude slower than an in-memory hash join.",
			batches, totalKB,
		)
	}

	suggestion := buildWorkMemSuggestion(batches, node.PeakMemoryUsage)

	return []advisor.Finding{{
		Severity:   advisor.Error,
		NodeID:     node.ID,
		NodeType:   node.NodeType,
		Message:    message,
		Detail:     detail,
		Suggestion: suggestion,
	}}
}

// buildWorkMemSuggestion constructs a work_mem recommendation.
// If peakKB is known, it computes the minimum work_mem needed for a
// single-batch join. Otherwise it gives a general recommendation.
func buildWorkMemSuggestion(batches int, peakKB *int64) string {
	if peakKB == nil {
		return "Increase work_mem to allow this hash join to execute entirely in memory. " +
			"Try doubling the current value and re-running EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)."
	}
	totalKB := int64(batches) * *peakKB
	totalMB := (totalKB + 1023) / 1024 // ceil to MB
	return fmt.Sprintf(
		"Increase work_mem to at least %dMB to allow this join to execute entirely in memory:\n"+
			"  SET work_mem = '%dMB';\n"+
			"For a permanent change, set work_mem = '%dMB' in postgresql.conf or per role:\n"+
			"  ALTER ROLE <role> SET work_mem = '%dMB';",
		totalMB, totalMB, totalMB, totalMB,
	)
}
