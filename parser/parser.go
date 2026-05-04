package parser

import (
	"encoding/json"
	"fmt"
)

// explainResult mirrors the JSON structure of one element in the top-level array
// that PostgreSQL emits. We keep it unexported because callers only need Plan.
type explainResult struct {
	Plan          Node    `json:"Plan"`
	PlanningTime  float64 `json:"Planning Time"`
	ExecutionTime float64 `json:"Execution Time"`
}

// Parse decodes the JSON produced by EXPLAIN (FORMAT JSON) or
// EXPLAIN (ANALYZE, FORMAT JSON) and returns the plan tree.
//
// data must be the raw bytes of the JSON output (the outer array included).
// Returns an error if the JSON is malformed or the array is empty.
func Parse(data []byte) (*Plan, error) {
	var results []explainResult
	if err := json.Unmarshal(data, &results); err != nil {
		return nil, fmt.Errorf("pgexplain: failed to parse EXPLAIN JSON: %w", err)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("pgexplain: EXPLAIN JSON contained no plan")
	}
	r := results[0]
	id := 1
	assignIDs(&r.Plan, &id)
	return &Plan{
		Node:          r.Plan,
		PlanningTime:  r.PlanningTime,
		ExecutionTime: r.ExecutionTime,
	}, nil
}

// assignIDs performs a depth-first pre-order walk and stamps each node with a
// unique integer ID starting at 1. It must be called after JSON unmarshaling,
// since IDs are not part of the EXPLAIN output.
func assignIDs(node *Node, next *int) {
	node.ID = *next
	*next++
	for i := range node.Plans {
		assignIDs(&node.Plans[i], next)
	}
}

// NodeByID returns the node with the given ID and true, or the zero value and
// false if no node with that ID exists. IDs are assigned by Parse() in
// depth-first pre-order starting at 1 (root = 1).
func (p *Plan) NodeByID(id int) (Node, bool) {
	return findNode(p.Node, id)
}

func findNode(node Node, id int) (Node, bool) {
	if node.ID == id {
		return node, true
	}
	for _, child := range node.Plans {
		if found, ok := findNode(child, id); ok {
			return found, true
		}
	}
	return Node{}, false
}
