package rules

import (
	"fmt"

	"github.com/bright98/pgexplain/advisor"
	"github.com/bright98/pgexplain/parser"
)

const defaultMinInnerLoops = 1000

// NestedLoopOption configures the NestedLoopLarge rule.
type NestedLoopOption func(*nestedLoopRule)

// WithMinInnerLoops sets the minimum number of inner-side executions that
// triggers a finding. Each execution corresponds to one outer row probing
// the inner side.
//
// Default: 1000.
func WithMinInnerLoops(n int) NestedLoopOption {
	return func(r *nestedLoopRule) {
		r.minInnerLoops = n
	}
}

// NestedLoopLarge returns a Rule that warns when a Nested Loop join executes
// its inner side an excessive number of times.
//
// # What is a Nested Loop join?
//
// A Nested Loop iterates over every outer row and, for each one, executes the
// inner side to find matches. The total work is:
//
//	outer_rows × cost_per_inner_probe
//
// When the inner side has an index on the join key, each probe is O(log n) —
// many probes can still be acceptable. When the inner side is a Seq Scan,
// each probe reads the entire inner table — the complexity is O(outer × inner),
// truly quadratic.
//
// The number of inner executions is visible as Actual Loops on the inner child
// node in EXPLAIN output.
//
// # Severity
//
// The severity depends on the inner node type:
//   - [advisor.Error] — inner side is a Seq Scan: every outer row triggers a
//     full table scan. Almost always a missing index on the join key.
//   - [advisor.Warn]  — inner side uses an index or another strategy: many probes
//     but each probe is efficient. May indicate a bad row estimate caused the
//     planner to prefer Nested Loop over Hash Join.
//
// # When does this rule fire?
//
// All conditions must hold:
//  1. Node type is Nested Loop.
//  2. ANALYZE was run (Actual Loops is present on the inner child).
//  3. An inner child exists (Parent Relationship == "Inner").
//  4. inner.ActualLoops >= minInnerLoops (default 1000).
//
// # Usage
//
//	rules.NestedLoopLarge()
//	rules.NestedLoopLarge(rules.WithMinInnerLoops(5000))
func NestedLoopLarge(opts ...NestedLoopOption) advisor.Rule {
	r := &nestedLoopRule{minInnerLoops: defaultMinInnerLoops}
	for _, o := range opts {
		o(r)
	}
	return r
}

type nestedLoopRule struct {
	minInnerLoops int
}

func (r *nestedLoopRule) Check(node parser.Node) []advisor.Finding {
	if node.NodeType != parser.NodeNestedLoop {
		return nil
	}

	inner := findInnerChild(node)
	if inner == nil {
		return nil
	}
	if inner.ActualLoops == nil {
		return nil // ANALYZE was not run
	}

	loops := int(*inner.ActualLoops)
	if loops < r.minInnerLoops {
		return nil
	}

	innerDesc := nodeDesc(*inner)
	isSeqScan := inner.NodeType == parser.NodeSeqScan

	severity := advisor.Warn
	if isSeqScan {
		severity = advisor.Error
	}

	var message string
	if isSeqScan {
		// Use relation name only — "full table scan on orders" reads better than
		// "full table scan on Seq Scan on orders".
		relation := "inner table"
		if inner.RelationName != nil {
			relation = fmt.Sprintf("%q", *inner.RelationName)
		}
		message = fmt.Sprintf(
			"nested loop performed full table scan on %s %d times",
			relation, loops,
		)
	} else {
		message = fmt.Sprintf(
			"nested loop probed %s %d times",
			innerDesc, loops,
		)
	}

	detail := buildNestedLoopDetail(inner, loops, isSeqScan)
	suggestion := buildNestedLoopSuggestion(node, inner, loops, isSeqScan)

	return []advisor.Finding{{
		Severity:   severity,
		NodeID:     node.ID,
		NodeType:   node.NodeType,
		Message:    message,
		Detail:     detail,
		Suggestion: suggestion,
	}}
}

// findInnerChild returns the first child with ParentRelationship == "Inner",
// or nil if no such child exists.
func findInnerChild(node parser.Node) *parser.Node {
	for i := range node.Plans {
		if node.Plans[i].ParentRelationship == "Inner" {
			return &node.Plans[i]
		}
	}
	return nil
}

// nodeDesc returns a human-readable description of a node for use in messages.
// Includes the relation name when present (scan nodes), omits it for operator
// nodes like Hash or Sort.
func nodeDesc(node parser.Node) string {
	if node.RelationName != nil {
		return fmt.Sprintf("%s on %q", node.NodeType, *node.RelationName)
	}
	return node.NodeType
}

func buildNestedLoopDetail(inner *parser.Node, loops int, isSeqScan bool) string {
	base := fmt.Sprintf(
		"The inner side of this Nested Loop was executed %d times — once per outer row.",
		loops,
	)

	if inner.ActualRows != nil {
		totalRows := int(*inner.ActualRows) * loops
		base += fmt.Sprintf(
			" Total rows produced by the inner side: %d (%d loops × %.0f rows per loop).",
			totalRows, loops, *inner.ActualRows,
		)
	}

	if isSeqScan {
		relation := "the inner table"
		if inner.RelationName != nil {
			relation = fmt.Sprintf("%q", *inner.RelationName)
		}
		base += fmt.Sprintf(
			" Because the inner side is a Seq Scan, PostgreSQL reads all rows of %s"+
				" on every iteration — O(outer × inner) complexity.",
			relation,
		)
	} else {
		base += " Each probe uses an index or other efficient strategy, but the cumulative" +
			" cost of many probes may exceed what a Hash Join or Merge Join would spend."
	}

	return base
}

func buildNestedLoopSuggestion(loop parser.Node, inner *parser.Node, loops int, isSeqScan bool) string {
	if isSeqScan {
		relation := "the inner table"
		if inner.RelationName != nil {
			relation = fmt.Sprintf("%q", *inner.RelationName)
		}
		cond := ""
		// The join condition lives on the Nested Loop node, not on the inner Seq Scan.
		if loop.JoinFilter != nil {
			cond = fmt.Sprintf(" to support the join filter %s", *loop.JoinFilter)
		}
		return fmt.Sprintf(
			"Add an index on %s%s. Without an index on the join key, each of the %d"+
				" outer rows triggers a full table scan. After adding the index, re-run"+
				" EXPLAIN (ANALYZE, FORMAT JSON) to confirm the plan changed to an Index Scan.",
			relation, cond, loops,
		)
	}
	return fmt.Sprintf(
		"The planner chose Nested Loop because it estimated a small outer set."+
			" With %d actual inner probes, a Hash Join or Merge Join may be more efficient."+
			" Check whether the outer row estimate is accurate — if not, run ANALYZE on"+
			" the relevant tables to refresh statistics.",
		loops,
	)
}
