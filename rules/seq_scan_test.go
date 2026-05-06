package rules

import (
	"strings"
	"testing"

	"github.com/Bright98/pgexplain/advisor"
	"github.com/Bright98/pgexplain/parser"
)

// helpers to build pointer fields inline in test cases.
func pf(v float64) *float64 { return &v }
func ps(v string) *string   { return &v }
func pi(v int64) *int64     { return &v }

func TestSeqScan_Check(t *testing.T) {
	tests := []struct {
		name          string
		node          parser.Node
		opts          []SeqScanOption
		wantFindings  int
		wantSeverity  advisor.Severity
		wantMsgSubstr string // non-empty: assert this substring appears in Message
	}{
		{
			name: "fires when ratio exceeds default threshold",
			node: parser.Node{
				ID:                  1,
				NodeType:            parser.NodeSeqScan,
				RelationName:        ps("orders"),
				Filter:              ps("(customer_id = 42)"),
				ActualRows:          pf(12),
				RowsRemovedByFilter: pi(99988), // ratio ~8332x
			},
			wantFindings:  1,
			wantSeverity:  advisor.Warn,
			wantMsgSubstr: "orders",
		},
		{
			name: "silent when ratio is below default threshold",
			node: parser.Node{
				ID:                  1,
				NodeType:            parser.NodeSeqScan,
				RelationName:        ps("orders"),
				Filter:              ps("(status = 'active')"),
				ActualRows:          pf(500),
				RowsRemovedByFilter: pi(100), // ratio 0.2x — well below 10
			},
			wantFindings: 0,
		},
		{
			name: "silent when ratio is just below threshold",
			node: parser.Node{
				ID:                  1,
				NodeType:            parser.NodeSeqScan,
				RelationName:        ps("orders"),
				Filter:              ps("(x = 1)"),
				ActualRows:          pf(10),
				RowsRemovedByFilter: pi(99), // ratio 9.9x — just below default 10
			},
			wantFindings: 0,
		},
		{
			name: "fires when ratio equals threshold exactly",
			node: parser.Node{
				ID:                  1,
				NodeType:            parser.NodeSeqScan,
				RelationName:        ps("orders"),
				Filter:              ps("(x = 1)"),
				ActualRows:          pf(10),
				RowsRemovedByFilter: pi(100), // ratio exactly 10x
			},
			wantFindings:  1,
			wantMsgSubstr: "10x",
		},
		{
			name: "silent when no filter (deliberate full read)",
			node: parser.Node{
				ID:           1,
				NodeType:     parser.NodeSeqScan,
				RelationName: ps("countries"),
				ActualRows:   pf(200),
				// RowsRemovedByFilter is nil
			},
			wantFindings: 0,
		},
		{
			name: "silent for non-seq-scan nodes",
			node: parser.Node{
				ID:                  1,
				NodeType:            parser.NodeIndexScan,
				RelationName:        ps("orders"),
				Filter:              ps("(x = 1)"),
				ActualRows:          pf(5),
				RowsRemovedByFilter: pi(50000),
			},
			wantFindings: 0,
		},
		{
			name: "fires when actual_rows is zero and removed exceeds raw threshold",
			node: parser.Node{
				ID:                  1,
				NodeType:            parser.NodeSeqScan,
				RelationName:        ps("orders"),
				Filter:              ps("(customer_id = -1)"),
				ActualRows:          pf(0),
				RowsRemovedByFilter: pi(50000),
			},
			wantFindings:  1,
			wantMsgSubstr: "returned none",
		},
		{
			name: "silent when actual_rows is zero but removed is below raw threshold",
			node: parser.Node{
				ID:                  1,
				NodeType:            parser.NodeSeqScan,
				RelationName:        ps("tiny"),
				Filter:              ps("(x = 99)"),
				ActualRows:          pf(0),
				RowsRemovedByFilter: pi(5), // 5 rows removed, tiny table
			},
			wantFindings: 0,
		},
		{
			name: "custom threshold fires at lower ratio",
			node: parser.Node{
				ID:                  1,
				NodeType:            parser.NodeSeqScan,
				RelationName:        ps("events"),
				Filter:              ps("(type = 'click')"),
				ActualRows:          pf(100),
				RowsRemovedByFilter: pi(700), // ratio 7x — below default 10, above custom 5
			},
			opts:         []SeqScanOption{WithMinFilterRatio(5)},
			wantFindings: 1,
			wantMsgSubstr: "events",
		},
		{
			name: "custom threshold does not fire when ratio is below it",
			node: parser.Node{
				ID:                  1,
				NodeType:            parser.NodeSeqScan,
				RelationName:        ps("events"),
				Filter:              ps("(type = 'click')"),
				ActualRows:          pf(100),
				RowsRemovedByFilter: pi(300), // ratio 3x — below custom 5
			},
			opts:         []SeqScanOption{WithMinFilterRatio(5)},
			wantFindings: 0,
		},
		{
			name: "finding fields are fully populated",
			node: parser.Node{
				ID:                  7,
				NodeType:            parser.NodeSeqScan,
				RelationName:        ps("orders"),
				Filter:              ps("(customer_id = 42)"),
				ActualRows:          pf(12),
				RowsRemovedByFilter: pi(99988),
			},
			wantFindings: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule := SeqScan(tt.opts...)
			findings := rule.Check(tt.node)

			if len(findings) != tt.wantFindings {
				t.Fatalf("Check() returned %d findings, want %d", len(findings), tt.wantFindings)
			}
			if tt.wantFindings == 0 {
				return
			}

			f := findings[0]

			if f.Severity != tt.wantSeverity && tt.wantSeverity != 0 {
				t.Errorf("Severity = %v, want %v", f.Severity, tt.wantSeverity)
			}
			if f.NodeID != tt.node.ID {
				t.Errorf("NodeID = %d, want %d", f.NodeID, tt.node.ID)
			}
			if f.NodeType != parser.NodeSeqScan {
				t.Errorf("NodeType = %q, want %q", f.NodeType, parser.NodeSeqScan)
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

// TestSeqScan_FindingContent spot-checks the exact wording of the finding
// for the common case (non-zero actual rows).
func TestSeqScan_FindingContent(t *testing.T) {
	node := parser.Node{
		ID:                  3,
		NodeType:            parser.NodeSeqScan,
		RelationName:        ps("orders"),
		Filter:              ps("(customer_id = 42)"),
		ActualRows:          pf(12),
		RowsRemovedByFilter: pi(99988),
	}
	findings := SeqScan().Check(node)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	f := findings[0]

	wantMsgSubstrings := []string{"orders", "8332x"}
	for _, s := range wantMsgSubstrings {
		if !strings.Contains(f.Message, s) {
			t.Errorf("Message %q does not contain %q", f.Message, s)
		}
	}
	wantDetailSubstrings := []string{"100000", "12", "(customer_id = 42)"}
	for _, s := range wantDetailSubstrings {
		if !strings.Contains(f.Detail, s) {
			t.Errorf("Detail %q does not contain %q", f.Detail, s)
		}
	}
	wantSuggestionSubstrings := []string{"orders", "(customer_id = 42)", "EXPLAIN"}
	for _, s := range wantSuggestionSubstrings {
		if !strings.Contains(f.Suggestion, s) {
			t.Errorf("Suggestion %q does not contain %q", f.Suggestion, s)
		}
	}
}
