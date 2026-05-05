package rules

import (
	"strings"
	"testing"

	"github.com/pgexplain/pgexplain/advisor"
	"github.com/pgexplain/pgexplain/parser"
)

func TestHashJoinSpill_Check(t *testing.T) {
	tests := []struct {
		name          string
		node          parser.Node
		wantFindings  int
		wantMsgSubstr string
	}{
		{
			name: "fires when hash batches > 1",
			node: parser.Node{
				ID:              3,
				NodeType:        parser.NodeHash,
				HashBatches:     intPtr(4),
				PeakMemoryUsage: pi(4096),
			},
			wantFindings:  1,
			wantMsgSubstr: "4 batches",
		},
		{
			name: "silent when hash batches == 1 (in memory)",
			node: parser.Node{
				ID:              3,
				NodeType:        parser.NodeHash,
				HashBatches:     intPtr(1),
				PeakMemoryUsage: pi(2048),
			},
			wantFindings: 0,
		},
		{
			name: "silent when hash batches is nil",
			node: parser.Node{
				ID:       3,
				NodeType: parser.NodeHash,
				// HashBatches nil — field absent in JSON (e.g. older PG version)
			},
			wantFindings: 0,
		},
		{
			name: "silent for non-Hash nodes",
			node: parser.Node{
				ID:          2,
				NodeType:    parser.NodeHashJoin,
				HashBatches: intPtr(4), // Hash Batches on Hash Join node is not what we check
			},
			wantFindings: 0,
		},
		{
			name: "fires when peak memory usage is nil (no BUFFERS)",
			node: parser.Node{
				ID:          3,
				NodeType:    parser.NodeHash,
				HashBatches: intPtr(8),
				// PeakMemoryUsage absent
			},
			wantFindings:  1,
			wantMsgSubstr: "8 batches",
		},
		{
			name: "message includes peak memory when present",
			node: parser.Node{
				ID:              3,
				NodeType:        parser.NodeHash,
				HashBatches:     intPtr(2),
				PeakMemoryUsage: pi(8192),
			},
			wantFindings:  1,
			wantMsgSubstr: "8192kB",
		},
		{
			name: "large spill fires and computes correct work_mem estimate",
			// batches=8, peak=4096kB → total=32768kB=32MB
			node: parser.Node{
				ID:              3,
				NodeType:        parser.NodeHash,
				HashBatches:     intPtr(8),
				PeakMemoryUsage: pi(4096),
			},
			wantFindings:  1,
			wantMsgSubstr: "8 batches",
		},
		{
			name: "two-batch spill (minimal spill) still fires",
			node: parser.Node{
				ID:              3,
				NodeType:        parser.NodeHash,
				HashBatches:     intPtr(2),
				PeakMemoryUsage: pi(4096),
			},
			wantFindings: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			findings := HashJoinSpill().Check(tt.node)

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
			if f.NodeType != parser.NodeHash {
				t.Errorf("NodeType = %q, want %q", f.NodeType, parser.NodeHash)
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

func TestHashJoinSpill_WorkMemSuggestion(t *testing.T) {
	tests := []struct {
		name             string
		batches          int
		peakKB           int64
		wantMBSubstr     string // expected MB value in suggestion
	}{
		{
			name:         "4 batches × 4096kB = 16384kB = 16MB",
			batches:      4,
			peakKB:       4096,
			wantMBSubstr: "16MB",
		},
		{
			name:         "8 batches × 4096kB = 32768kB = 32MB",
			batches:      8,
			peakKB:       4096,
			wantMBSubstr: "32MB",
		},
		{
			name:         "2 batches × 2048kB = 4096kB = 4MB",
			batches:      2,
			peakKB:       2048,
			wantMBSubstr: "4MB",
		},
		{
			name:         "ceil: 3 batches × 1000kB = 3000kB → 3MB (ceil from 2.92)",
			batches:      3,
			peakKB:       1000,
			wantMBSubstr: "3MB",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			peak := tt.peakKB
			suggestion := buildWorkMemSuggestion(tt.batches, &peak)
			if !strings.Contains(suggestion, tt.wantMBSubstr) {
				t.Errorf("suggestion %q does not contain %q", suggestion, tt.wantMBSubstr)
			}
		})
	}
}

func TestHashJoinSpill_FindingContent(t *testing.T) {
	// batches=4, peak=4096kB → total=16384kB → suggest 16MB
	node := parser.Node{
		ID:              3,
		NodeType:        parser.NodeHash,
		HashBatches:     intPtr(4),
		PeakMemoryUsage: pi(4096),
	}
	findings := HashJoinSpill().Check(node)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	f := findings[0]

	wantMsgSubstrings := []string{"4 batches", "4096kB"}
	for _, s := range wantMsgSubstrings {
		if !strings.Contains(f.Message, s) {
			t.Errorf("Message %q does not contain %q", f.Message, s)
		}
	}
	wantDetailSubstrings := []string{"4", "16384kB", "disk"}
	for _, s := range wantDetailSubstrings {
		if !strings.Contains(f.Detail, s) {
			t.Errorf("Detail %q does not contain %q", f.Detail, s)
		}
	}
	wantSuggestionSubstrings := []string{"16MB", "work_mem", "SET work_mem"}
	for _, s := range wantSuggestionSubstrings {
		if !strings.Contains(f.Suggestion, s) {
			t.Errorf("Suggestion %q does not contain %q", f.Suggestion, s)
		}
	}
}

// intPtr is a helper for *int in tests (distinct from pi which returns *int64).
func intPtr(v int) *int { return &v }
