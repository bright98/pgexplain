package rules_test

import (
	"strings"
	"testing"

	"github.com/pgexplain/pgexplain/parser"
	"github.com/pgexplain/pgexplain/rules"
)

func pf64(v float64) *float64 { return &v }
func pi64(v int64) *int64     { return &v }
func ps(v string) *string     { return &v }
func pint(v int) *int         { return &v }

func TestMissingIndexOnlyScan(t *testing.T) {
	rel := "users"
	idx := "users_email_idx"

	cases := []struct {
		name        string
		node        parser.Node
		opts        []rules.IndexOnlyScanOption
		wantFindings int
		wantPct     string // substring to find in message, empty means don't check
	}{
		{
			name: "fires when heap fetch ratio exceeds default threshold",
			node: parser.Node{
				NodeType:    parser.NodeIndexOnlyScan,
				RelationName: &rel,
				IndexName:   &idx,
				PlanRows:    500,
				ActualRows:  pf64(500),
				ActualLoops: pf64(1),
				HeapFetches: pi64(200), // 200/500 = 40%
			},
			wantFindings: 1,
			wantPct:      "40%",
		},
		{
			name: "fires at exactly the threshold (10%)",
			node: parser.Node{
				NodeType:    parser.NodeIndexOnlyScan,
				RelationName: &rel,
				PlanRows:    100,
				ActualRows:  pf64(100),
				ActualLoops: pf64(1),
				HeapFetches: pi64(10), // 10/100 = 10%
			},
			wantFindings: 1,
		},
		{
			name: "silent when heap fetch ratio is below threshold",
			node: parser.Node{
				NodeType:    parser.NodeIndexOnlyScan,
				RelationName: &rel,
				PlanRows:    1000,
				ActualRows:  pf64(1000),
				ActualLoops: pf64(1),
				HeapFetches: pi64(5), // 5/1000 = 0.5%
			},
			wantFindings: 0,
		},
		{
			name: "silent when HeapFetches is 0 (fully covered)",
			node: parser.Node{
				NodeType:    parser.NodeIndexOnlyScan,
				RelationName: &rel,
				PlanRows:    1000,
				ActualRows:  pf64(1000),
				ActualLoops: pf64(1),
				HeapFetches: pi64(0),
			},
			wantFindings: 0,
		},
		{
			name: "silent when ANALYZE was not run (ActualRows nil)",
			node: parser.Node{
				NodeType:    parser.NodeIndexOnlyScan,
				RelationName: &rel,
				PlanRows:    1000,
				HeapFetches: pi64(800),
				// ActualRows and ActualLoops are nil
			},
			wantFindings: 0,
		},
		{
			name: "silent when HeapFetches field is absent",
			node: parser.Node{
				NodeType:    parser.NodeIndexOnlyScan,
				RelationName: &rel,
				PlanRows:    1000,
				ActualRows:  pf64(1000),
				ActualLoops: pf64(1),
				// HeapFetches is nil
			},
			wantFindings: 0,
		},
		{
			name: "silent on non-index-only-scan node",
			node: parser.Node{
				NodeType:    parser.NodeIndexScan,
				RelationName: &rel,
				PlanRows:    1000,
				ActualRows:  pf64(1000),
				ActualLoops: pf64(1),
				HeapFetches: pi64(999),
			},
			wantFindings: 0,
		},
		{
			name: "accounts for multiple loops correctly",
			// 50 actual rows per loop, 10 loops = 500 true total
			// HeapFetches = 300 → 300/500 = 60%
			node: parser.Node{
				NodeType:    parser.NodeIndexOnlyScan,
				RelationName: &rel,
				PlanRows:    500,
				ActualRows:  pf64(50),
				ActualLoops: pf64(10),
				HeapFetches: pi64(300),
			},
			wantFindings: 1,
			wantPct:      "60%",
		},
		{
			name: "custom threshold: fires at ratio above custom value",
			node: parser.Node{
				NodeType:    parser.NodeIndexOnlyScan,
				RelationName: &rel,
				PlanRows:    100,
				ActualRows:  pf64(100),
				ActualLoops: pf64(1),
				HeapFetches: pi64(60), // 60%
			},
			opts:         []rules.IndexOnlyScanOption{rules.WithMinHeapFetchRatio(0.5)},
			wantFindings: 1,
		},
		{
			name: "custom threshold: silent when ratio is below custom value",
			node: parser.Node{
				NodeType:    parser.NodeIndexOnlyScan,
				RelationName: &rel,
				PlanRows:    100,
				ActualRows:  pf64(100),
				ActualLoops: pf64(1),
				HeapFetches: pi64(30), // 30% — below 50% threshold
			},
			opts:         []rules.IndexOnlyScanOption{rules.WithMinHeapFetchRatio(0.5)},
			wantFindings: 0,
		},
		{
			name: "silent when both actual rows and heap fetches are zero",
			node: parser.Node{
				NodeType:    parser.NodeIndexOnlyScan,
				RelationName: &rel,
				PlanRows:    0,
				ActualRows:  pf64(0),
				ActualLoops: pf64(1),
				HeapFetches: pi64(0),
			},
			wantFindings: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.node.ID = 1
			rule := rules.MissingIndexOnlyScan(tc.opts...)
			findings := rule.Check(tc.node)

			if len(findings) != tc.wantFindings {
				t.Fatalf("got %d findings, want %d", len(findings), tc.wantFindings)
			}
			if tc.wantFindings == 0 {
				return
			}

			f := findings[0]
			if f.NodeID != 1 {
				t.Errorf("NodeID = %d, want 1", f.NodeID)
			}
			if f.NodeType != parser.NodeIndexOnlyScan {
				t.Errorf("NodeType = %q, want %q", f.NodeType, parser.NodeIndexOnlyScan)
			}
			if tc.wantPct != "" && !strings.Contains(f.Message, tc.wantPct) {
				t.Errorf("Message %q does not contain %q", f.Message, tc.wantPct)
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
