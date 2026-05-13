// Package advisor runs a set of rules over a parsed plan tree and collects findings.
package advisor

import "github.com/bright98/pgexplain/parser"

// Severity describes how serious a finding is.
type Severity int

const (
	Info  Severity = iota // informational; no action required
	Warn                  // something worth investigating
	Error                 // likely a real performance problem
)

// String returns the uppercase label for a severity level.
func (s Severity) String() string {
	switch s {
	case Info:
		return "INFO"
	case Warn:
		return "WARN"
	case Error:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

// Finding is one piece of feedback emitted by a rule for a specific plan node.
type Finding struct {
	Severity   Severity
	NodeID     int      // ID of the node that triggered this; look up via Plan.NodeByID
	NodeType   string   // convenience copy of the node's type; avoids a lookup for display
	Message    string   // short one-line summary
	Detail     string   // longer explanation of why this is a problem
	Suggestion string   // what to do about it
}

// Rule inspects a single plan node and returns any findings.
// Returning nil or an empty slice means the node looks fine for this rule.
type Rule interface {
	Check(node parser.Node) []Finding
}

// Advisor runs a fixed set of rules over every node in a plan tree.
type Advisor struct {
	rules []Rule
}

// New creates an Advisor that applies the given rules to every node it visits.
func New(rules ...Rule) *Advisor {
	return &Advisor{rules: rules}
}

// Analyze walks the full plan tree depth-first and returns all findings across
// all nodes and all rules. The order of findings matches the pre-order traversal
// of the tree (same order as node IDs).
func (a *Advisor) Analyze(plan *parser.Plan) []Finding {
	return walk(plan.Node, a.rules)
}

// walk visits every node in the plan tree using an explicit stack instead of
// recursion. Nodes are processed in depth-first pre-order (parent before
// children, left sibling before right), which matches the node ID assignment
// order from the parser.
func walk(root parser.Node, rules []Rule) []Finding {
	var findings []Finding
	stack := []parser.Node{root}
	for len(stack) > 0 {
		// pop from the top
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		for _, r := range rules {
			findings = append(findings, r.Check(node)...)
		}

		// push children right-to-left so the leftmost child is processed first
		for i := len(node.Plans) - 1; i >= 0; i-- {
			stack = append(stack, node.Plans[i])
		}
	}
	return findings
}
