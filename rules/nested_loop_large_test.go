package rules

import (
	"strings"
	"testing"

	"github.com/pgexplain/pgexplain/advisor"
	"github.com/pgexplain/pgexplain/parser"
)

// nestedLoopNode builds a Nested Loop node with the given outer and inner children.
func nestedLoopNode(id int, outer, inner parser.Node) parser.Node {
	return parser.Node{
		ID:          id,
		NodeType:    parser.NodeNestedLoop,
		PlanRows:    float64(100),
		ActualRows:  pf(100),
		ActualLoops: pf(1),
		Plans:       []parser.Node{outer, inner},
	}
}

func outerSeqScan() parser.Node {
	return parser.Node{
		ID:                  2,
		NodeType:            parser.NodeSeqScan,
		ParentRelationship:  "Outer",
		RelationName:        ps("orders"),
		PlanRows:            1000,
		ActualRows:          pf(1000),
		ActualLoops:         pf(1),
	}
}

func innerIndexScan(loops float64) parser.Node {
	return parser.Node{
		ID:                 3,
		NodeType:           parser.NodeIndexScan,
		ParentRelationship: "Inner",
		RelationName:       ps("order_items"),
		IndexName:          ps("order_items_order_id_idx"),
		IndexCond:          ps("(order_id = o.id)"),
		PlanRows:           3,
		ActualRows:         pf(3),
		ActualLoops:        pf(loops),
	}
}

func innerSeqScan(loops float64) parser.Node {
	return parser.Node{
		ID:                 3,
		NodeType:           parser.NodeSeqScan,
		ParentRelationship: "Inner",
		RelationName:       ps("order_items"),
		PlanRows:           5000,
		ActualRows:         pf(3),
		ActualLoops:        pf(loops),
	}
}

func TestNestedLoopLarge_Check(t *testing.T) {
	tests := []struct {
		name          string
		node          parser.Node
		opts          []NestedLoopOption
		wantFindings  int
		wantSeverity  advisor.Severity
		wantMsgSubstr string
	}{
		{
			name:          "Warn when inner is Index Scan and loops >= threshold",
			node:          nestedLoopNode(1, outerSeqScan(), innerIndexScan(1000)),
			wantFindings:  1,
			wantSeverity:  advisor.Warn,
			wantMsgSubstr: "1000 times",
		},
		{
			name:          "Error when inner is Seq Scan and loops >= threshold",
			node:          nestedLoopNode(1, outerSeqScan(), innerSeqScan(1000)),
			wantFindings:  1,
			wantSeverity:  advisor.Error,
			wantMsgSubstr: "full table scan",
		},
		{
			name:         "silent when inner loops below default threshold",
			node:         nestedLoopNode(1, outerSeqScan(), innerIndexScan(999)),
			wantFindings: 0,
		},
		{
			name:         "silent when inner loops exactly at threshold",
			node:         nestedLoopNode(1, outerSeqScan(), innerIndexScan(1000)),
			wantFindings: 1, // >= fires at threshold
		},
		{
			name:         "silent when not a Nested Loop node",
			node: parser.Node{
				ID:       1,
				NodeType: parser.NodeHashJoin,
				Plans:    []parser.Node{outerSeqScan(), innerIndexScan(1000)},
			},
			wantFindings: 0,
		},
		{
			name: "silent when inner ActualLoops is nil (no ANALYZE)",
			node: nestedLoopNode(1, outerSeqScan(), parser.Node{
				ID:                 3,
				NodeType:           parser.NodeIndexScan,
				ParentRelationship: "Inner",
				RelationName:       ps("order_items"),
				// ActualLoops nil
			}),
			wantFindings: 0,
		},
		{
			name: "silent when no inner child exists",
			node: parser.Node{
				ID:       1,
				NodeType: parser.NodeNestedLoop,
				Plans: []parser.Node{
					{
						ID:                 2,
						NodeType:           parser.NodeSeqScan,
						ParentRelationship: "Outer",
						ActualLoops:        pf(1),
					},
					// no "Inner" child
				},
			},
			wantFindings: 0,
		},
		{
			name: "silent when Plans is empty",
			node: parser.Node{
				ID:       1,
				NodeType: parser.NodeNestedLoop,
			},
			wantFindings: 0,
		},
		{
			name:         "custom threshold fires at lower loop count",
			node:         nestedLoopNode(1, outerSeqScan(), innerIndexScan(200)),
			opts:         []NestedLoopOption{WithMinInnerLoops(100)},
			wantFindings: 1,
			wantSeverity: advisor.Warn,
		},
		{
			name:         "custom threshold does not fire below it",
			node:         nestedLoopNode(1, outerSeqScan(), innerIndexScan(50)),
			opts:         []NestedLoopOption{WithMinInnerLoops(100)},
			wantFindings: 0,
		},
		{
			name:          "Index Scan inner message says 'probed', not 'full table scan'",
			node:          nestedLoopNode(1, outerSeqScan(), innerIndexScan(2000)),
			wantFindings:  1,
			wantSeverity:  advisor.Warn,
			wantMsgSubstr: "probed",
		},
		{
			name:          "Seq Scan inner message says 'full table scan', not 'probed'",
			node:          nestedLoopNode(1, outerSeqScan(), innerSeqScan(2000)),
			wantFindings:  1,
			wantSeverity:  advisor.Error,
			wantMsgSubstr: "full table scan",
		},
		{
			name:          "finding NodeID is the Nested Loop node, not the inner child",
			node:          nestedLoopNode(7, outerSeqScan(), innerIndexScan(1000)),
			wantFindings:  1,
			wantMsgSubstr: "order_items",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			findings := NestedLoopLarge(tt.opts...).Check(tt.node)

			if len(findings) != tt.wantFindings {
				t.Fatalf("Check() returned %d findings, want %d", len(findings), tt.wantFindings)
			}
			if tt.wantFindings == 0 {
				return
			}

			f := findings[0]
			if tt.wantSeverity != 0 && f.Severity != tt.wantSeverity {
				t.Errorf("Severity = %v, want %v", f.Severity, tt.wantSeverity)
			}
			if f.NodeID != tt.node.ID {
				t.Errorf("NodeID = %d, want %d (should be Nested Loop node, not inner child)", f.NodeID, tt.node.ID)
			}
			if f.NodeType != parser.NodeNestedLoop {
				t.Errorf("NodeType = %q, want %q", f.NodeType, parser.NodeNestedLoop)
			}
			if tt.wantMsgSubstr != "" && !strings.Contains(f.Message, tt.wantMsgSubstr) {
				t.Errorf("Message = %q, want it to contain %q", f.Message, tt.wantMsgSubstr)
			}
			if f.Message == "" {
				t.Error("Message is empty")
			}
			if f.Detail == "" {
				t.Error("Detail is empty")
			}
			if f.Suggestion == "" {
				t.Error("Suggestion is empty")
			}
		})
	}
}

func TestNestedLoopLarge_FindingContent_IndexScan(t *testing.T) {
	// inner=Index Scan, loops=1000, actual rows per loop=3 → total=3000
	node := nestedLoopNode(1, outerSeqScan(), innerIndexScan(1000))
	findings := NestedLoopLarge().Check(node)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	f := findings[0]

	if f.Severity != advisor.Warn {
		t.Errorf("Severity = %v, want Warn", f.Severity)
	}
	for _, s := range []string{"order_items", "1000"} {
		if !strings.Contains(f.Message, s) {
			t.Errorf("Message %q does not contain %q", f.Message, s)
		}
	}
	for _, s := range []string{"1000", "3000", "3 rows per loop"} {
		if !strings.Contains(f.Detail, s) {
			t.Errorf("Detail %q does not contain %q", f.Detail, s)
		}
	}
	for _, s := range []string{"Hash Join", "ANALYZE"} {
		if !strings.Contains(f.Suggestion, s) {
			t.Errorf("Suggestion %q does not contain %q", f.Suggestion, s)
		}
	}
}

func TestNestedLoopLarge_FindingContent_SeqScan(t *testing.T) {
	// inner=Seq Scan, loops=1000 → Error + index suggestion
	node := nestedLoopNode(1, outerSeqScan(), innerSeqScan(1000))
	findings := NestedLoopLarge().Check(node)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	f := findings[0]

	if f.Severity != advisor.Error {
		t.Errorf("Severity = %v, want Error", f.Severity)
	}
	for _, s := range []string{"order_items", "1000"} {
		if !strings.Contains(f.Message, s) {
			t.Errorf("Message %q does not contain %q", f.Message, s)
		}
	}
	for _, s := range []string{"Seq Scan", "O(outer × inner)"} {
		if !strings.Contains(f.Detail, s) {
			t.Errorf("Detail %q does not contain %q", f.Detail, s)
		}
	}
	for _, s := range []string{"index", "1000", "EXPLAIN"} {
		if !strings.Contains(f.Suggestion, s) {
			t.Errorf("Suggestion %q does not contain %q", f.Suggestion, s)
		}
	}
}
