package rules_test

import (
	"strings"
	"testing"

	"github.com/Bright98/pgexplain/parser"
	"github.com/Bright98/pgexplain/rules"
)

func TestParallelNotLaunched(t *testing.T) {
	cases := []struct {
		name         string
		node         parser.Node
		wantFindings int
		wantInMsg    string
	}{
		{
			name: "fires on Gather when launched < planned",
			node: parser.Node{
				NodeType:        parser.NodeGather,
				WorkersPlanned:  pint(4),
				WorkersLaunched: pint(2),
			},
			wantFindings: 1,
			wantInMsg:    "2 of 4 planned workers",
		},
		{
			name: "fires on Gather Merge when launched < planned",
			node: parser.Node{
				NodeType:        parser.NodeGatherMerge,
				WorkersPlanned:  pint(4),
				WorkersLaunched: pint(1),
			},
			wantFindings: 1,
			wantInMsg:    "1 of 4 planned workers",
		},
		{
			name: "fires when zero workers launched (all workers failed)",
			node: parser.Node{
				NodeType:        parser.NodeGather,
				WorkersPlanned:  pint(2),
				WorkersLaunched: pint(0),
			},
			wantFindings: 1,
			wantInMsg:    "0 of 2 planned workers",
		},
		{
			name: "silent when launched equals planned (full parallelism achieved)",
			node: parser.Node{
				NodeType:        parser.NodeGather,
				WorkersPlanned:  pint(4),
				WorkersLaunched: pint(4),
			},
			wantFindings: 0,
		},
		{
			name: "silent when launched exceeds planned (should not happen but handled)",
			node: parser.Node{
				NodeType:        parser.NodeGather,
				WorkersPlanned:  pint(2),
				WorkersLaunched: pint(3),
			},
			wantFindings: 0,
		},
		{
			name: "silent when Workers Planned is zero",
			node: parser.Node{
				NodeType:        parser.NodeGather,
				WorkersPlanned:  pint(0),
				WorkersLaunched: pint(0),
			},
			wantFindings: 0,
		},
		{
			name: "silent when WorkersPlanned is nil",
			node: parser.Node{
				NodeType:        parser.NodeGather,
				WorkersLaunched: pint(2),
			},
			wantFindings: 0,
		},
		{
			name: "silent when WorkersLaunched is nil",
			node: parser.Node{
				NodeType:       parser.NodeGather,
				WorkersPlanned: pint(4),
			},
			wantFindings: 0,
		},
		{
			name: "silent on non-gather node",
			node: parser.Node{
				NodeType:        parser.NodeSeqScan,
				WorkersPlanned:  pint(4),
				WorkersLaunched: pint(2),
			},
			wantFindings: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.node.ID = 1
			rule := rules.ParallelNotLaunched()
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

func TestParallelNotLaunched_detailMentionsZeroWorkers(t *testing.T) {
	node := parser.Node{
		ID:              1,
		NodeType:        parser.NodeGather,
		WorkersPlanned:  pint(4),
		WorkersLaunched: pint(0),
	}

	findings := rules.ParallelNotLaunched().Check(node)
	if len(findings) != 1 {
		t.Fatalf("got %d findings, want 1", len(findings))
	}
	if !strings.Contains(findings[0].Detail, "leader process") {
		t.Errorf("Detail should mention leader process when 0 workers launched:\n%s", findings[0].Detail)
	}
}

func TestParallelNotLaunched_suggestionMentionsGUCs(t *testing.T) {
	node := parser.Node{
		ID:              1,
		NodeType:        parser.NodeGather,
		WorkersPlanned:  pint(4),
		WorkersLaunched: pint(2),
	}

	findings := rules.ParallelNotLaunched().Check(node)
	if len(findings) != 1 {
		t.Fatalf("got %d findings, want 1", len(findings))
	}

	sugg := findings[0].Suggestion
	for _, want := range []string{
		"max_parallel_workers",
		"max_parallel_workers_per_gather",
		"pg_reload_conf",
	} {
		if !strings.Contains(sugg, want) {
			t.Errorf("Suggestion does not contain %q:\n%s", want, sugg)
		}
	}
}
