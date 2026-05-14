package rules_test

import (
	"strings"
	"testing"

	"github.com/bright98/pgexplain/parser"
	"github.com/bright98/pgexplain/rules"
)

func TestHighTempBlockIO(t *testing.T) {
	cases := []struct {
		name         string
		node         parser.Node
		opts         []rules.HighTempBlockIOOption
		wantFindings int
		wantInMsg    string
	}{
		{
			name: "fires on canonical case (HashAggregate with high temp blocks)",
			node: parser.Node{
				NodeType:          parser.NodeAggregate,
				TempWrittenBlocks: pi64(9800),
				TempReadBlocks:    pi64(9800),
			},
			wantFindings: 1,
			wantInMsg:    "9800 temp blocks",
		},
		{
			name: "fires at exactly the threshold",
			node: parser.Node{
				NodeType:          parser.NodeAggregate,
				TempWrittenBlocks: pi64(256),
				TempReadBlocks:    pi64(256),
			},
			wantFindings: 1,
		},
		{
			name: "silent just below the threshold",
			node: parser.Node{
				NodeType:          parser.NodeAggregate,
				TempWrittenBlocks: pi64(255),
				TempReadBlocks:    pi64(255),
			},
			wantFindings: 0,
		},
		{
			name: "silent when both fields are nil (BUFFERS not used)",
			node: parser.Node{
				NodeType: parser.NodeAggregate,
				// TempWrittenBlocks and TempReadBlocks both nil
			},
			wantFindings: 0,
		},
		{
			name: "silent on Sort node (covered by SortSpill)",
			node: parser.Node{
				NodeType:          parser.NodeSort,
				TempWrittenBlocks: pi64(9800),
				TempReadBlocks:    pi64(9800),
			},
			wantFindings: 0,
		},
		{
			name: "silent on Hash node (covered by HashJoinSpill)",
			node: parser.Node{
				NodeType:          parser.NodeHash,
				TempWrittenBlocks: pi64(9800),
				TempReadBlocks:    pi64(9800),
			},
			wantFindings: 0,
		},
		{
			name: "silent when temp blocks are zero (BUFFERS present but no spill)",
			node: parser.Node{
				NodeType:          parser.NodeAggregate,
				TempWrittenBlocks: pi64(0),
				TempReadBlocks:    pi64(0),
			},
			wantFindings: 0,
		},
		{
			name: "custom threshold: fires above",
			node: parser.Node{
				NodeType:          parser.NodeAggregate,
				TempWrittenBlocks: pi64(1024),
				TempReadBlocks:    pi64(1024),
			},
			opts:         []rules.HighTempBlockIOOption{rules.WithMinTempBlocks(1024)},
			wantFindings: 1,
		},
		{
			name: "custom threshold: silent below",
			node: parser.Node{
				NodeType:          parser.NodeAggregate,
				TempWrittenBlocks: pi64(1023),
				TempReadBlocks:    pi64(1023),
			},
			opts:         []rules.HighTempBlockIOOption{rules.WithMinTempBlocks(1024)},
			wantFindings: 0,
		},
		{
			name: "uses max: read > write — fires when read exceeds threshold",
			node: parser.Node{
				NodeType:          parser.NodeAggregate,
				TempWrittenBlocks: pi64(100), // below threshold
				TempReadBlocks:    pi64(300), // above threshold
			},
			wantFindings: 1,
			wantInMsg:    "300 temp blocks",
		},
		{
			name: "uses max: write > read — fires when write exceeds threshold",
			node: parser.Node{
				NodeType:          parser.NodeAggregate,
				TempWrittenBlocks: pi64(300), // above threshold
				TempReadBlocks:    pi64(100), // below threshold
			},
			wantFindings: 1,
			wantInMsg:    "300 temp blocks",
		},
		{
			name: "fires when only TempWrittenBlocks is set",
			node: parser.Node{
				NodeType:          parser.NodeAggregate,
				TempWrittenBlocks: pi64(500),
				// TempReadBlocks nil
			},
			wantFindings: 1,
		},
		{
			name: "fires when only TempReadBlocks is set",
			node: parser.Node{
				NodeType: parser.NodeAggregate,
				// TempWrittenBlocks nil
				TempReadBlocks: pi64(500),
			},
			wantFindings: 1,
		},
		{
			name: "message includes node type",
			node: parser.Node{
				NodeType:          parser.NodeSubqueryScan,
				TempWrittenBlocks: pi64(1000),
				TempReadBlocks:    pi64(1000),
			},
			wantFindings: 1,
			wantInMsg:    parser.NodeSubqueryScan,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.node.ID = 1
			rule := rules.HighTempBlockIO(tc.opts...)
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
			if f.NodeType != tc.node.NodeType {
				t.Errorf("NodeType = %q, want %q", f.NodeType, tc.node.NodeType)
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

func TestHighTempBlockIO_DetailContent(t *testing.T) {
	node := parser.Node{
		ID:                1,
		NodeType:          parser.NodeAggregate,
		TempWrittenBlocks: pi64(9800),
		TempReadBlocks:    pi64(9800),
	}
	findings := rules.HighTempBlockIO().Check(node)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	f := findings[0]

	for _, want := range []string{"9800", "work_mem", "Aggregate"} {
		if !strings.Contains(f.Detail, want) {
			t.Errorf("Detail %q does not contain %q", f.Detail, want)
		}
	}
}

func TestHighTempBlockIO_MBCalculation(t *testing.T) {
	cases := []struct {
		blocks    int64
		wantMBStr string
	}{
		{blocks: 128, wantMBStr: "1MB"},  // 128*8=1024kB=1MB exactly
		{blocks: 129, wantMBStr: "2MB"},  // 129*8=1032kB → ceil = 2MB
		{blocks: 256, wantMBStr: "2MB"},  // 256*8=2048kB=2MB exactly
		{blocks: 9800, wantMBStr: "77MB"}, // 9800*8=78400kB → ceil = 77MB
	}

	for _, tc := range cases {
		node := parser.Node{
			ID:                1,
			NodeType:          parser.NodeAggregate,
			TempWrittenBlocks: pi64(tc.blocks),
			TempReadBlocks:    pi64(tc.blocks),
		}
		findings := rules.HighTempBlockIO(rules.WithMinTempBlocks(1)).Check(node)
		if len(findings) != 1 {
			t.Fatalf("blocks=%d: got %d findings, want 1", tc.blocks, len(findings))
		}
		if !strings.Contains(findings[0].Message, tc.wantMBStr) {
			t.Errorf("blocks=%d: message %q does not contain %q",
				tc.blocks, findings[0].Message, tc.wantMBStr)
		}
		if !strings.Contains(findings[0].Suggestion, tc.wantMBStr) {
			t.Errorf("blocks=%d: suggestion %q does not contain %q",
				tc.blocks, findings[0].Suggestion, tc.wantMBStr)
		}
	}
}
