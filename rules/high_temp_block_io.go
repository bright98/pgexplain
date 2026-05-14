package rules

import (
	"fmt"

	"github.com/bright98/pgexplain/advisor"
	"github.com/bright98/pgexplain/parser"
)

const defaultMinTempBlocks int64 = 256

// HighTempBlockIOOption configures the HighTempBlockIO rule.
type HighTempBlockIOOption func(*highTempBlockIORule)

// WithMinTempBlocks sets the minimum number of temporary blocks (read or
// written) required to trigger a finding. One PostgreSQL block is 8kB, so the
// default of 256 corresponds to approximately 2MB of temporary disk I/O.
// Default: 256.
func WithMinTempBlocks(n int64) HighTempBlockIOOption {
	return func(r *highTempBlockIORule) {
		r.minTempBlocks = n
	}
}

// HighTempBlockIO returns a Rule that warns when any plan node writes or reads
// an excessive number of temporary disk blocks due to exceeding work_mem.
//
// # What is temporary block I/O?
//
// When PostgreSQL executes an operation that buffers intermediate rows — an
// aggregation, a window function, a materialized CTE, or similar — it allocates
// memory from work_mem. When the working set exceeds that budget, PostgreSQL
// writes the overflow to temporary files on disk. These temporary blocks are
// tracked separately from regular table I/O:
//
//   - Temp Written Blocks: blocks written to temp files
//   - Temp Read Blocks: blocks read back from temp files
//
// In a normal spill the two counts are equal: every block written is eventually
// read back. These fields only appear when EXPLAIN is run with the BUFFERS option.
//
// This rule skips Sort and Hash nodes, which are already covered by the SortSpill
// and HashJoinSpill rules with richer node-specific diagnostics. It catches all
// other node types — notably HashAggregate, WindowAgg, SubqueryScan, and CTE Scan
// — that can also spill to disk.
//
// # What does this rule check?
//
//	IF node is not a Sort or Hash node
//	AND TempWrittenBlocks or TempReadBlocks is non-nil (BUFFERS was used)
//	AND max(TempWrittenBlocks, TempReadBlocks) >= minTempBlocks (default 256)
//	→ emit Warn finding
//
// # When does this rule fire?
//
// All conditions must hold:
//  1. Node type is neither Sort nor Hash.
//  2. At least one of TempWrittenBlocks or TempReadBlocks is non-nil.
//  3. max(TempWrittenBlocks, TempReadBlocks) >= minTempBlocks.
//
// # Usage
//
//	rules.HighTempBlockIO()
//	rules.HighTempBlockIO(rules.WithMinTempBlocks(1024)) // ≈ 8MB
func HighTempBlockIO(opts ...HighTempBlockIOOption) advisor.Rule {
	r := &highTempBlockIORule{minTempBlocks: defaultMinTempBlocks}
	for _, o := range opts {
		o(r)
	}
	return r
}

type highTempBlockIORule struct {
	minTempBlocks int64
}

func (r *highTempBlockIORule) Check(node parser.Node) []advisor.Finding {
	// Sort and Hash have dedicated rules with richer diagnostics.
	if node.NodeType == parser.NodeSort || node.NodeType == parser.NodeHash {
		return nil
	}
	if node.TempWrittenBlocks == nil && node.TempReadBlocks == nil {
		return nil // BUFFERS option was not used
	}

	var written, read int64
	if node.TempWrittenBlocks != nil {
		written = *node.TempWrittenBlocks
	}
	if node.TempReadBlocks != nil {
		read = *node.TempReadBlocks
	}

	maxBlocks := written
	if read > maxBlocks {
		maxBlocks = read
	}
	if maxBlocks < r.minTempBlocks {
		return nil
	}

	return []advisor.Finding{{
		Severity:   advisor.Warn,
		NodeID:     node.ID,
		NodeType:   node.NodeType,
		Message:    buildHighTempBlockIOMessage(node.NodeType, maxBlocks),
		Detail:     buildHighTempBlockIODetail(node.NodeType, written, read, maxBlocks),
		Suggestion: buildHighTempBlockIOSuggestion(node.NodeType, maxBlocks),
	}}
}

func buildHighTempBlockIOMessage(nodeType string, maxBlocks int64) string {
	return fmt.Sprintf("%s spilled to disk using %d temp blocks (≈ %dMB)",
		nodeType, maxBlocks, tempBlocksToMB(maxBlocks))
}

func buildHighTempBlockIODetail(nodeType string, written, read, maxBlocks int64) string {
	detail := fmt.Sprintf(
		"This %s node wrote intermediate results to temporary disk files because its "+
			"working set exceeded work_mem. Each block written to disk must be read back "+
			"before the node completes, adding sequential I/O that in-memory execution avoids.",
		nodeType,
	)
	detail += fmt.Sprintf(" Temp blocks written: %d, read: %d (≈ %dMB).",
		written, read, tempBlocksToMB(maxBlocks))
	return detail
}

func buildHighTempBlockIOSuggestion(nodeType string, maxBlocks int64) string {
	mb := tempBlocksToMB(maxBlocks)
	return fmt.Sprintf(
		"Increase work_mem to at least %dMB to allow this %s to run in memory:\n"+
			"  SET work_mem = '%dMB';\n"+
			"For a permanent change, set it per role or in postgresql.conf:\n"+
			"  ALTER ROLE <role> SET work_mem = '%dMB';\n"+
			"Note: work_mem is a per-operation budget. A query with multiple sorts\n"+
			"or hash joins can use it several times simultaneously.",
		mb, nodeType, mb, mb,
	)
}

// tempBlocksToMB converts a block count to approximate megabytes, rounding up.
// PostgreSQL's default block size is 8kB.
func tempBlocksToMB(blocks int64) int64 {
	return (blocks*8 + 1023) / 1024
}
