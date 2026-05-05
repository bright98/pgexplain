package rules

import (
	"strings"
	"testing"

	"github.com/pgexplain/pgexplain/advisor"
	"github.com/pgexplain/pgexplain/parser"
)

func TestRowEstimateMismatch_Check(t *testing.T) {
	tests := []struct {
		name          string
		node          parser.Node
		opts          []RowEstimateMismatchOption
		wantFindings  int
		wantMsgSubstr string
	}{
		{
			name: "fires on underestimate above default threshold",
			node: parser.Node{
				ID:          1,
				NodeType:    parser.NodeSeqScan,
				PlanRows:    15,
				ActualRows:  pf(12000),
				ActualLoops: pf(1),
			},
			wantFindings:  1,
			wantMsgSubstr: "underestimate",
		},
		{
			name: "fires on overestimate above default threshold",
			node: parser.Node{
				ID:          1,
				NodeType:    parser.NodeHashJoin,
				PlanRows:    50000,
				ActualRows:  pf(3),
				ActualLoops: pf(1),
			},
			wantFindings:  1,
			wantMsgSubstr: "overestimate",
		},
		{
			name: "silent when factor is below default threshold",
			node: parser.Node{
				ID:          1,
				NodeType:    parser.NodeSeqScan,
				PlanRows:    100,
				ActualRows:  pf(150),
				ActualLoops: pf(1), // factor 1.5x — well below 10
			},
			wantFindings: 0,
		},
		{
			name: "silent when both sides are below minRows floor",
			node: parser.Node{
				ID:          1,
				NodeType:    parser.NodeSeqScan,
				PlanRows:    1,
				ActualRows:  pf(15),
				ActualLoops: pf(1), // factor 15x but max=15 < 100
			},
			wantFindings: 0,
		},
		{
			name: "fires when actual side alone exceeds minRows floor",
			node: parser.Node{
				ID:          1,
				NodeType:    parser.NodeSeqScan,
				PlanRows:    1,
				ActualRows:  pf(5000),
				ActualLoops: pf(1), // max=5000 >= 100, factor=5000x
			},
			wantFindings:  1,
			wantMsgSubstr: "5000x",
		},
		{
			name: "fires when plan side alone exceeds minRows floor",
			node: parser.Node{
				ID:          1,
				NodeType:    parser.NodeSort,
				PlanRows:    500,
				ActualRows:  pf(0),
				ActualLoops: pf(1), // plan=500 >= 100, actual=0 → denominator clamped to 1
			},
			wantFindings:  1,
			wantMsgSubstr: "overestimate",
		},
		{
			name: "silent when both sides are zero",
			node: parser.Node{
				ID:          1,
				NodeType:    parser.NodeSeqScan,
				PlanRows:    0,
				ActualRows:  pf(0),
				ActualLoops: pf(1),
			},
			wantFindings: 0,
		},
		{
			name: "accounts for Actual Loops when computing true actual rows",
			// Inside a Nested Loop, a node may execute many times.
			// plan=10, actual_per_loop=50, loops=100 → trueActual=5000, factor=500x
			node: parser.Node{
				ID:          3,
				NodeType:    parser.NodeIndexScan,
				PlanRows:    10,
				ActualRows:  pf(50),
				ActualLoops: pf(100),
			},
			wantFindings:  1,
			wantMsgSubstr: "500x",
		},
		{
			name: "silent when actual loops bring true count below threshold",
			// plan=200, actual_per_loop=200, loops=1 → trueActual=200, factor=1x
			node: parser.Node{
				ID:          1,
				NodeType:    parser.NodeSeqScan,
				PlanRows:    200,
				ActualRows:  pf(200),
				ActualLoops: pf(1),
			},
			wantFindings: 0,
		},
		{
			name: "silent when ANALYZE was not run (ActualRows nil)",
			node: parser.Node{
				ID:       1,
				NodeType: parser.NodeSeqScan,
				PlanRows: 15,
				// ActualRows and ActualLoops are nil — EXPLAIN without ANALYZE
			},
			wantFindings: 0,
		},
		{
			name: "fires on non-leaf nodes (Hash Join, Sort, etc.)",
			node: parser.Node{
				ID:          2,
				NodeType:    parser.NodeHashJoin,
				PlanRows:    10,
				ActualRows:  pf(8000),
				ActualLoops: pf(1),
			},
			wantFindings:  1,
			wantMsgSubstr: "Hash Join",
		},
		{
			name: "custom factor threshold fires at lower ratio",
			node: parser.Node{
				ID:          1,
				NodeType:    parser.NodeSeqScan,
				PlanRows:    100,
				ActualRows:  pf(400),
				ActualLoops: pf(1), // factor 4x — below default 10, above custom 3
			},
			opts:         []RowEstimateMismatchOption{WithMinEstimateFactor(3)},
			wantFindings: 1,
		},
		{
			name: "custom minRows floor suppresses finding on small node",
			node: parser.Node{
				ID:          1,
				NodeType:    parser.NodeSeqScan,
				PlanRows:    1,
				ActualRows:  pf(150),
				ActualLoops: pf(1), // max=150 >= default 100, fires by default
			},
			opts:         []RowEstimateMismatchOption{WithMinRows(500)},
			wantFindings: 0, // max=150 < custom floor 500 → silent
		},
		{
			name: "factor computed symmetrically (over vs under gives same factor)",
			// plan=10000, actual=100 → factor=100x overestimate
			// plan=100, actual=10000 → factor=100x underestimate
			// Both should fire with wantFindings=1
			node: parser.Node{
				ID:          1,
				NodeType:    parser.NodeAggregate,
				PlanRows:    10000,
				ActualRows:  pf(100),
				ActualLoops: pf(1),
			},
			wantFindings:  1,
			wantMsgSubstr: "100x",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule := RowEstimateMismatch(tt.opts...)
			findings := rule.Check(tt.node)

			if len(findings) != tt.wantFindings {
				t.Fatalf("Check() returned %d findings, want %d", len(findings), tt.wantFindings)
			}
			if tt.wantFindings == 0 {
				return
			}

			f := findings[0]
			if f.Severity != advisor.Warn {
				t.Errorf("Severity = %v, want Warn", f.Severity)
			}
			if f.NodeID != tt.node.ID {
				t.Errorf("NodeID = %d, want %d", f.NodeID, tt.node.ID)
			}
			if f.NodeType != tt.node.NodeType {
				t.Errorf("NodeType = %q, want %q", f.NodeType, tt.node.NodeType)
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

func TestRowEstimateMismatch_FindingContent(t *testing.T) {
	// plan=15, actual=12000, loops=1 → factor=800x underestimate
	node := parser.Node{
		ID:          3,
		NodeType:    parser.NodeSeqScan,
		PlanRows:    15,
		ActualRows:  pf(12000),
		ActualLoops: pf(1),
	}
	findings := RowEstimateMismatch().Check(node)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	f := findings[0]

	wantMsgSubstrings := []string{"800x", "underestimate", "15", "12000"}
	for _, s := range wantMsgSubstrings {
		if !strings.Contains(f.Message, s) {
			t.Errorf("Message %q does not contain %q", f.Message, s)
		}
	}
	wantDetailSubstrings := []string{"15", "12000", "loops=1", "800x"}
	for _, s := range wantDetailSubstrings {
		if !strings.Contains(f.Detail, s) {
			t.Errorf("Detail %q does not contain %q", f.Detail, s)
		}
	}
	wantSuggestionSubstrings := []string{"ANALYZE", "STATISTICS"}
	for _, s := range wantSuggestionSubstrings {
		if !strings.Contains(f.Suggestion, s) {
			t.Errorf("Suggestion %q does not contain %q", f.Suggestion, s)
		}
	}
}

func TestRowEstimateMismatch_LoopsMultiplication(t *testing.T) {
	// Verify the rule multiplies ActualRows by ActualLoops before comparing.
	// Without multiplication: plan=10, actual_per_loop=50 → factor=5x (silent)
	// With multiplication: plan=10, actual_total=5000 → factor=500x (fires)
	node := parser.Node{
		ID:          1,
		NodeType:    parser.NodeIndexScan,
		PlanRows:    10,
		ActualRows:  pf(50),   // per loop
		ActualLoops: pf(100),  // 100 executions
	}
	findings := RowEstimateMismatch().Check(node)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding (loops must be accounted for), got %d", len(findings))
	}
	if !strings.Contains(findings[0].Message, "500x") {
		t.Errorf("expected factor 500x in message, got: %s", findings[0].Message)
	}
	if !strings.Contains(findings[0].Detail, "loops=100") {
		t.Errorf("expected loops=100 in detail, got: %s", findings[0].Detail)
	}
}
