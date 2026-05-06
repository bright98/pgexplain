package rules

import (
	"fmt"

	"github.com/pgexplain/pgexplain/advisor"
	"github.com/pgexplain/pgexplain/parser"
)

// ParallelNotLaunched returns a Rule that warns when a Gather or Gather Merge
// node launched fewer parallel workers than the planner intended.
//
// # What is parallel query execution?
//
// PostgreSQL can split a query across multiple worker processes to use more CPU
// cores simultaneously. The plan shows this as a Gather or Gather Merge node —
// a synchronization point where the leader process collects results from workers.
//
// Two fields on the Gather node indicate whether parallelism was achieved:
//
//   - Workers Planned: how many workers the planner expected to use
//   - Workers Launched: how many actually started at runtime
//
// When Workers Launched < Workers Planned, the query ran with less parallelism
// than the planner assumed when building the plan. Costs and row estimates in
// the plan are based on Workers Planned — if fewer workers ran, those estimates
// no longer reflect reality and the query likely took longer than the planner
// predicted.
//
// # What does this rule check?
//
//	IF node is Gather or Gather Merge
//	AND Workers Planned > 0
//	AND Workers Launched < Workers Planned
//	→ emit Warn finding
//
// There is no configurable threshold — any gap between planned and launched
// is worth flagging. The most common causes are hitting max_parallel_workers
// (the global cap across all queries) or max_parallel_workers_per_gather
// (the per-Gather cap).
//
// # When does this rule fire?
//
// All conditions must hold:
//  1. Node type is Gather or Gather Merge.
//  2. Workers Planned is non-nil and greater than zero.
//  3. Workers Launched is non-nil and less than Workers Planned.
//
// # Usage
//
//	rules.ParallelNotLaunched()
func ParallelNotLaunched() advisor.Rule {
	return &parallelNotLaunchedRule{}
}

type parallelNotLaunchedRule struct{}

func (r *parallelNotLaunchedRule) Check(node parser.Node) []advisor.Finding {
	if node.NodeType != parser.NodeGather && node.NodeType != parser.NodeGatherMerge {
		return nil
	}
	if node.WorkersPlanned == nil || node.WorkersLaunched == nil {
		return nil
	}

	planned := *node.WorkersPlanned
	launched := *node.WorkersLaunched

	if planned == 0 || launched >= planned {
		return nil
	}

	message := fmt.Sprintf(
		"%s launched %d of %d planned workers",
		node.NodeType, launched, planned,
	)

	detail := buildParallelDetail(node, planned, launched)
	suggestion := buildParallelSuggestion(planned, launched)

	return []advisor.Finding{{
		Severity:   advisor.Warn,
		NodeID:     node.ID,
		NodeType:   node.NodeType,
		Message:    message,
		Detail:     detail,
		Suggestion: suggestion,
	}}
}

func buildParallelDetail(node parser.Node, planned, launched int) string {
	missed := planned - launched

	detail := fmt.Sprintf(
		"The planner designed this %s plan for %d parallel worker(s), "+
			"but only %d launched at runtime (%d short). "+
			"Plan costs and row estimates assume %d workers — "+
			"with fewer workers running, the query likely took longer than predicted.",
		node.NodeType, planned, launched, missed, planned,
	)

	if launched == 0 {
		detail += " No workers launched at all — the parallel section ran entirely in the leader process."
	}

	return detail
}

func buildParallelSuggestion(planned, launched int) string {
	return fmt.Sprintf(
		"Check whether the parallel worker limits were hit at runtime:\n"+
			"  SHOW max_parallel_workers;            -- global cap across all queries (current: check vs %d planned)\n"+
			"  SHOW max_parallel_workers_per_gather; -- per-Gather cap\n"+
			"If either value is below %d, raise it:\n"+
			"  ALTER SYSTEM SET max_parallel_workers = %d;\n"+
			"  ALTER SYSTEM SET max_parallel_workers_per_gather = %d;\n"+
			"  SELECT pg_reload_conf();\n"+
			"Also check whether other concurrent queries are consuming the worker budget:\n"+
			"  SELECT count(*) FROM pg_stat_activity WHERE backend_type = 'parallel worker';",
		planned, planned, planned, planned,
	)
}
