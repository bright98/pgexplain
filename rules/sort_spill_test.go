package rules_test

import (
	"strings"
	"testing"

	"github.com/Bright98/pgexplain/parser"
	"github.com/Bright98/pgexplain/rules"
)

func TestSortSpill(t *testing.T) {
	disk := "Disk"
	memory := "Memory"
	extMerge := "external merge"
	quicksort := "quicksort"

	cases := []struct {
		name         string
		node         parser.Node
		wantFindings int
		wantInMsg    string // substring expected in message
	}{
		{
			name: "fires when Sort Space Type is Disk",
			node: parser.Node{
				NodeType:      parser.NodeSort,
				SortSpaceType: &disk,
				SortMethod:    &extMerge,
				SortSpaceUsed: pi64(8192),
			},
			wantFindings: 1,
			wantInMsg:    "external merge",
		},
		{
			name: "message includes disk usage in kB",
			node: parser.Node{
				NodeType:      parser.NodeSort,
				SortSpaceType: &disk,
				SortMethod:    &extMerge,
				SortSpaceUsed: pi64(18432),
			},
			wantFindings: 1,
			wantInMsg:    "18432 kB",
		},
		{
			name: "fires when SortMethod is absent but SortSpaceType is Disk",
			node: parser.Node{
				NodeType:      parser.NodeSort,
				SortSpaceType: &disk,
				// SortMethod nil — should still fire, fall back to "external merge"
			},
			wantFindings: 1,
		},
		{
			name: "silent when Sort Space Type is Memory (in-memory quicksort)",
			node: parser.Node{
				NodeType:      parser.NodeSort,
				SortSpaceType: &memory,
				SortMethod:    &quicksort,
				SortSpaceUsed: pi64(4096),
			},
			wantFindings: 0,
		},
		{
			name: "silent when SortSpaceType is nil (ANALYZE not run)",
			node: parser.Node{
				NodeType:   parser.NodeSort,
				SortMethod: &extMerge,
				// SortSpaceType nil
			},
			wantFindings: 0,
		},
		{
			name: "silent on non-sort node",
			node: parser.Node{
				NodeType:      parser.NodeSeqScan,
				SortSpaceType: &disk,
			},
			wantFindings: 0,
		},
		{
			name: "suggestion includes rounded-up work_mem target",
			// 18432 kB → 18 MB (ceiling)
			node: parser.Node{
				NodeType:      parser.NodeSort,
				SortSpaceType: &disk,
				SortMethod:    &extMerge,
				SortSpaceUsed: pi64(18432),
			},
			wantFindings: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.node.ID = 1
			rule := rules.SortSpill()
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

func TestSortSpill_workMemCalculation(t *testing.T) {
	disk := "Disk"

	cases := []struct {
		diskKB      int64
		wantMemMB   int64
		wantInSugg  string
	}{
		{diskKB: 1024, wantMemMB: 1, wantInSugg: "1MB"},
		{diskKB: 1025, wantMemMB: 2, wantInSugg: "2MB"}, // round up
		{diskKB: 8192, wantMemMB: 8, wantInSugg: "8MB"},
		{diskKB: 18432, wantMemMB: 18, wantInSugg: "18MB"},
		{diskKB: 18433, wantMemMB: 19, wantInSugg: "19MB"}, // round up
	}

	for _, tc := range cases {
		node := parser.Node{
			ID:            1,
			NodeType:      parser.NodeSort,
			SortSpaceType: &disk,
			SortSpaceUsed: pi64(tc.diskKB),
		}

		findings := rules.SortSpill().Check(node)
		if len(findings) != 1 {
			t.Fatalf("diskKB=%d: got %d findings, want 1", tc.diskKB, len(findings))
		}
		if !strings.Contains(findings[0].Suggestion, tc.wantInSugg) {
			t.Errorf("diskKB=%d: suggestion %q does not contain %q",
				tc.diskKB, findings[0].Suggestion, tc.wantInSugg)
		}
	}
}
