package rules_test

import (
	"strings"
	"testing"

	"github.com/bright98/pgexplain/advisor"
	"github.com/bright98/pgexplain/parser"
	"github.com/bright98/pgexplain/rules"
)

func TestTopNHeapsort(t *testing.T) {
	topN := "top-N heapsort"
	extMerge := "external merge"
	rel := "orders"

	seqScanChild := func(actualRows float64, loops float64) parser.Node {
		return parser.Node{
			NodeType:    parser.NodeSeqScan,
			RelationName: &rel,
			ActualRows:  &actualRows,
			ActualLoops: &loops,
		}
	}

	cases := []struct {
		name         string
		node         parser.Node
		opts         []rules.TopNHeapsortOption
		wantFindings int
		wantSeverity advisor.Severity
		wantInMsg    string
	}{
		{
			name: "fires when top-N heapsort over large Seq Scan",
			node: parser.Node{
				NodeType:    parser.NodeSort,
				SortMethod:  &topN,
				SortKey:     []string{"created_at DESC"},
				ActualRows:  pf64(10),
				ActualLoops: pf64(1),
				Plans:       []parser.Node{seqScanChild(5000, 1)},
			},
			wantFindings: 1,
			wantSeverity: advisor.Info,
			wantInMsg:    "5000 rows",
		},
		{
			name: "message includes rows returned when ActualRows present",
			node: parser.Node{
				NodeType:    parser.NodeSort,
				SortMethod:  &topN,
				SortKey:     []string{"created_at DESC"},
				ActualRows:  pf64(10),
				ActualLoops: pf64(1),
				Plans:       []parser.Node{seqScanChild(50000, 1)},
			},
			wantFindings: 1,
			wantInMsg:    "to return 10",
		},
		{
			name: "accounts for multiple child loops",
			// child: 200 rows/loop × 10 loops = 2000 total
			node: parser.Node{
				NodeType:   parser.NodeSort,
				SortMethod: &topN,
				SortKey:    []string{"name ASC"},
				Plans:      []parser.Node{seqScanChild(200, 10)},
			},
			wantFindings: 1,
			wantInMsg:    "2000 rows",
		},
		{
			name: "silent when child rows below default threshold (999)",
			node: parser.Node{
				NodeType:   parser.NodeSort,
				SortMethod: &topN,
				Plans:      []parser.Node{seqScanChild(999, 1)},
			},
			wantFindings: 0,
		},
		{
			name: "fires at exactly the default threshold (1000)",
			node: parser.Node{
				NodeType:   parser.NodeSort,
				SortMethod: &topN,
				Plans:      []parser.Node{seqScanChild(1000, 1)},
			},
			wantFindings: 1,
		},
		{
			name: "silent when Sort Method is external merge (different rule)",
			node: parser.Node{
				NodeType:   parser.NodeSort,
				SortMethod: &extMerge,
				Plans:      []parser.Node{seqScanChild(50000, 1)},
			},
			wantFindings: 0,
		},
		{
			name: "silent when SortMethod is nil",
			node: parser.Node{
				NodeType: parser.NodeSort,
				Plans:    []parser.Node{seqScanChild(50000, 1)},
			},
			wantFindings: 0,
		},
		{
			name: "silent when child is Index Scan (not Seq Scan)",
			node: parser.Node{
				NodeType:   parser.NodeSort,
				SortMethod: &topN,
				Plans: []parser.Node{{
					NodeType:     parser.NodeIndexScan,
					RelationName: &rel,
					ActualRows:   pf64(50000),
					ActualLoops:  pf64(1),
				}},
			},
			wantFindings: 0,
		},
		{
			name: "silent when ANALYZE was not run on child (ActualRows nil)",
			node: parser.Node{
				NodeType:   parser.NodeSort,
				SortMethod: &topN,
				Plans: []parser.Node{{
					NodeType:     parser.NodeSeqScan,
					RelationName: &rel,
					// ActualRows and ActualLoops nil
				}},
			},
			wantFindings: 0,
		},
		{
			name: "silent when node has no children",
			node: parser.Node{
				NodeType:   parser.NodeSort,
				SortMethod: &topN,
			},
			wantFindings: 0,
		},
		{
			name: "silent on non-sort node",
			node: parser.Node{
				NodeType: parser.NodeSeqScan,
			},
			wantFindings: 0,
		},
		{
			name: "custom threshold: fires above custom value",
			node: parser.Node{
				NodeType:   parser.NodeSort,
				SortMethod: &topN,
				Plans:      []parser.Node{seqScanChild(5000, 1)},
			},
			opts:         []rules.TopNHeapsortOption{rules.WithMinInputRows(2000)},
			wantFindings: 1,
		},
		{
			name: "custom threshold: silent when below custom value",
			node: parser.Node{
				NodeType:   parser.NodeSort,
				SortMethod: &topN,
				Plans:      []parser.Node{seqScanChild(1999, 1)},
			},
			opts:         []rules.TopNHeapsortOption{rules.WithMinInputRows(2000)},
			wantFindings: 0,
		},
		{
			name: "suggestion includes CREATE INDEX with sort key",
			node: parser.Node{
				NodeType:   parser.NodeSort,
				SortMethod: &topN,
				SortKey:    []string{"created_at DESC"},
				Plans:      []parser.Node{seqScanChild(10000, 1)},
			},
			wantFindings: 1,
			wantInMsg:    "", // checked separately below
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.node.ID = 1
			rule := rules.TopNHeapsort(tc.opts...)
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
			if f.NodeType != parser.NodeSort {
				t.Errorf("NodeType = %q, want %q", f.NodeType, parser.NodeSort)
			}
			if f.Severity != advisor.Info {
				t.Errorf("Severity = %v, want Info", f.Severity)
			}
			if tc.wantInMsg != "" && !strings.Contains(f.Message, tc.wantInMsg) {
				t.Errorf("Message %q does not contain %q", f.Message, tc.wantInMsg)
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

func TestTopNHeapsort_suggestionContainsCreateIndex(t *testing.T) {
	topN := "top-N heapsort"
	rel := "orders"

	node := parser.Node{
		ID:         1,
		NodeType:   parser.NodeSort,
		SortMethod: &topN,
		SortKey:    []string{"created_at DESC", "id ASC"},
		Plans: []parser.Node{{
			NodeType:    parser.NodeSeqScan,
			RelationName: &rel,
			ActualRows:  pf64(50000),
			ActualLoops: pf64(1),
		}},
	}

	findings := rules.TopNHeapsort().Check(node)
	if len(findings) != 1 {
		t.Fatalf("got %d findings, want 1", len(findings))
	}

	sugg := findings[0].Suggestion
	if !strings.Contains(sugg, "CREATE INDEX") {
		t.Errorf("Suggestion does not contain CREATE INDEX:\n%s", sugg)
	}
	if !strings.Contains(sugg, "created_at DESC") {
		t.Errorf("Suggestion does not contain sort key 'created_at DESC':\n%s", sugg)
	}
	if !strings.Contains(sugg, "ANALYZE orders") {
		t.Errorf("Suggestion does not contain ANALYZE step:\n%s", sugg)
	}
}
