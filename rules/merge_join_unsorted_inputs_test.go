package rules_test

import (
	"strings"
	"testing"

	"github.com/bright98/pgexplain/parser"
	"github.com/bright98/pgexplain/rules"
)

func TestMergeJoinUnsortedInputs(t *testing.T) {
	cases := []struct {
		name         string
		node         parser.Node
		opts         []rules.MergeJoinUnsortedInputsOption
		wantFindings int
		wantInMsg    string
	}{
		{
			name: "fires when both inputs are sorted",
			node: parser.Node{
				NodeType: parser.NodeMergeJoin,
				Plans: []parser.Node{
					{NodeType: parser.NodeSort, ParentRelationship: "Outer", PlanRows: 48000, SortKey: []string{"o.customer_id"}},
					{NodeType: parser.NodeSort, ParentRelationship: "Inner", PlanRows: 25000, SortKey: []string{"c.id"}},
				},
			},
			wantFindings: 1,
			wantInMsg:    "both inputs",
		},
		{
			name: "fires when only outer is sorted",
			node: parser.Node{
				NodeType: parser.NodeMergeJoin,
				Plans: []parser.Node{
					{NodeType: parser.NodeSort, ParentRelationship: "Outer", PlanRows: 5000, SortKey: []string{"o.id"}},
					{NodeType: parser.NodeIndexScan, ParentRelationship: "Inner", PlanRows: 1000},
				},
			},
			wantFindings: 1,
			wantInMsg:    "outer input",
		},
		{
			name: "fires when only inner is sorted",
			node: parser.Node{
				NodeType: parser.NodeMergeJoin,
				Plans: []parser.Node{
					{NodeType: parser.NodeIndexScan, ParentRelationship: "Outer", PlanRows: 1000},
					{NodeType: parser.NodeSort, ParentRelationship: "Inner", PlanRows: 5000, SortKey: []string{"b.id"}},
				},
			},
			wantFindings: 1,
			wantInMsg:    "inner input",
		},
		{
			name: "fires at exactly the min-rows threshold",
			node: parser.Node{
				NodeType: parser.NodeMergeJoin,
				Plans: []parser.Node{
					{NodeType: parser.NodeSort, ParentRelationship: "Outer", PlanRows: 1000, SortKey: []string{"a.id"}},
				},
			},
			opts:         []rules.MergeJoinUnsortedInputsOption{rules.WithMinMergeJoinSortRows(1000)},
			wantFindings: 1,
		},
		{
			name: "silent just below the min-rows threshold",
			node: parser.Node{
				NodeType: parser.NodeMergeJoin,
				Plans: []parser.Node{
					{NodeType: parser.NodeSort, ParentRelationship: "Outer", PlanRows: 999, SortKey: []string{"a.id"}},
				},
			},
			opts:         []rules.MergeJoinUnsortedInputsOption{rules.WithMinMergeJoinSortRows(1000)},
			wantFindings: 0,
		},
		{
			name: "silent when no Sort children (both are Index Scans)",
			node: parser.Node{
				NodeType: parser.NodeMergeJoin,
				Plans: []parser.Node{
					{NodeType: parser.NodeIndexScan, ParentRelationship: "Outer"},
					{NodeType: parser.NodeIndexScan, ParentRelationship: "Inner"},
				},
			},
			wantFindings: 0,
		},
		{
			name: "silent on wrong node type",
			node: parser.Node{
				NodeType: parser.NodeHashJoin,
				Plans: []parser.Node{
					{NodeType: parser.NodeSort, ParentRelationship: "Outer"},
				},
			},
			wantFindings: 0,
		},
		{
			name: "fires without ANALYZE (SortSpaceType absent)",
			node: parser.Node{
				NodeType: parser.NodeMergeJoin,
				Plans: []parser.Node{
					{
						NodeType:           parser.NodeSort,
						ParentRelationship: "Outer",
						PlanRows:           10000,
						SortKey:            []string{"t.id"},
						// SortSpaceType nil — ANALYZE not run
					},
				},
			},
			wantFindings: 1,
			wantInMsg:    "outer input",
		},
		{
			name: "custom threshold: fires above",
			node: parser.Node{
				NodeType: parser.NodeMergeJoin,
				Plans: []parser.Node{
					{NodeType: parser.NodeSort, ParentRelationship: "Outer", PlanRows: 5000, SortKey: []string{"a.x"}},
				},
			},
			opts:         []rules.MergeJoinUnsortedInputsOption{rules.WithMinMergeJoinSortRows(1000)},
			wantFindings: 1,
		},
		{
			name: "custom threshold: silent below",
			node: parser.Node{
				NodeType: parser.NodeMergeJoin,
				Plans: []parser.Node{
					{NodeType: parser.NodeSort, ParentRelationship: "Outer", PlanRows: 500, SortKey: []string{"a.x"}},
				},
			},
			opts:         []rules.MergeJoinUnsortedInputsOption{rules.WithMinMergeJoinSortRows(1000)},
			wantFindings: 0,
		},
		{
			name: "threshold uses max across both Sort children",
			// outer < threshold but inner >= threshold — should fire
			node: parser.Node{
				NodeType: parser.NodeMergeJoin,
				Plans: []parser.Node{
					{NodeType: parser.NodeSort, ParentRelationship: "Outer", PlanRows: 500, SortKey: []string{"a.id"}},
					{NodeType: parser.NodeSort, ParentRelationship: "Inner", PlanRows: 2000, SortKey: []string{"b.id"}},
				},
			},
			opts:         []rules.MergeJoinUnsortedInputsOption{rules.WithMinMergeJoinSortRows(1000)},
			wantFindings: 1,
		},
		{
			name: "detail includes disk spill info when SortSpaceType is Disk",
			node: parser.Node{
				NodeType: parser.NodeMergeJoin,
				Plans: []parser.Node{
					{
						NodeType:           parser.NodeSort,
						ParentRelationship: "Outer",
						PlanRows:           48000,
						SortKey:            []string{"o.customer_id"},
						SortSpaceType:      ps("Disk"),
						SortSpaceUsed:      pi64(14336),
					},
				},
			},
			wantFindings: 1,
			wantInMsg:    "outer",
		},
		{
			name: "silent when Plans is nil",
			node: parser.Node{
				NodeType: parser.NodeMergeJoin,
			},
			wantFindings: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.node.ID = 1
			rule := rules.MergeJoinUnsortedInputs(tc.opts...)
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
			if f.NodeType != parser.NodeMergeJoin {
				t.Errorf("NodeType = %q, want %q", f.NodeType, parser.NodeMergeJoin)
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

func TestMergeJoinUnsortedInputs_DetailContent(t *testing.T) {
	node := parser.Node{
		ID:       1,
		NodeType: parser.NodeMergeJoin,
		Plans: []parser.Node{
			{
				NodeType:           parser.NodeSort,
				ParentRelationship: "Outer",
				PlanRows:           48000,
				SortKey:            []string{"o.customer_id"},
				SortSpaceType:      ps("Disk"),
				SortSpaceUsed:      pi64(14336),
			},
			{
				NodeType:           parser.NodeSort,
				ParentRelationship: "Inner",
				PlanRows:           25000,
				SortKey:            []string{"c.id"},
				SortSpaceType:      ps("Memory"),
				SortSpaceUsed:      pi64(892),
			},
		},
	}

	findings := rules.MergeJoinUnsortedInputs().Check(node)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	f := findings[0]

	for _, want := range []string{"48000", "o.customer_id", "14336 kB", "spilled to disk"} {
		if !strings.Contains(f.Detail, want) {
			t.Errorf("Detail %q does not contain %q", f.Detail, want)
		}
	}
	for _, want := range []string{"25000", "c.id"} {
		if !strings.Contains(f.Detail, want) {
			t.Errorf("Detail %q does not contain %q", f.Detail, want)
		}
	}
}

func TestMergeJoinUnsortedInputs_SuggestionContent(t *testing.T) {
	node := parser.Node{
		ID:       1,
		NodeType: parser.NodeMergeJoin,
		Plans: []parser.Node{
			{NodeType: parser.NodeSort, ParentRelationship: "Outer", PlanRows: 1000, SortKey: []string{"o.customer_id"}},
			{NodeType: parser.NodeSort, ParentRelationship: "Inner", PlanRows: 500, SortKey: []string{"c.id"}},
		},
	}

	findings := rules.MergeJoinUnsortedInputs().Check(node)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	f := findings[0]

	// Aliases should be stripped in the CREATE INDEX suggestion.
	for _, want := range []string{"CREATE INDEX", "customer_id", "id", "EXPLAIN"} {
		if !strings.Contains(f.Suggestion, want) {
			t.Errorf("Suggestion %q does not contain %q", f.Suggestion, want)
		}
	}
	// Full qualified names (with alias prefix) should not appear.
	for _, notWant := range []string{"o.customer_id", "c.id"} {
		if strings.Contains(f.Suggestion, notWant) {
			t.Errorf("Suggestion %q should not contain %q (alias should be stripped)", f.Suggestion, notWant)
		}
	}
}
