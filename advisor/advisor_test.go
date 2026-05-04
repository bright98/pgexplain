package advisor

import (
	"testing"

	"github.com/pgexplain/pgexplain/parser"
)

// seqScanRule fires a Warn finding on every Seq Scan node.
// Used in tests as a minimal real-looking rule.
type seqScanRule struct{}

func (seqScanRule) Check(node parser.Node) []Finding {
	if node.NodeType != parser.NodeSeqScan {
		return nil
	}
	return []Finding{{
		Severity:   Warn,
		NodeID:     node.ID,
		NodeType:   node.NodeType,
		Message:    "sequential scan detected",
		Detail:     "A sequential scan reads every row in the table.",
		Suggestion: "Consider adding an index if this table is large.",
	}}
}

// collectingRule records every node type it sees without emitting findings.
// Used to verify the walker visits all nodes.
type collectingRule struct {
	visited []string
}

func (r *collectingRule) Check(node parser.Node) []Finding {
	r.visited = append(r.visited, node.NodeType)
	return nil
}

func mustParse(t *testing.T, data []byte) *parser.Plan {
	t.Helper()
	plan, err := parser.Parse(data)
	if err != nil {
		t.Fatalf("parser.Parse() error = %v", err)
	}
	return plan
}

func TestAnalyze_NoRules(t *testing.T) {
	plan := mustParse(t, seqScanJSON)
	findings := New().Analyze(plan)
	if len(findings) != 0 {
		t.Errorf("Analyze() with no rules = %d findings, want 0", len(findings))
	}
}

func TestAnalyze_NoopRule(t *testing.T) {
	plan := mustParse(t, seqScanJSON)
	// ExampleRule equivalent inline
	noop := ruleFunc(func(_ parser.Node) []Finding { return nil })
	findings := New(noop).Analyze(plan)
	if len(findings) != 0 {
		t.Errorf("Analyze() with noop rule = %d findings, want 0", len(findings))
	}
}

func TestAnalyze_WalkerVisitsAllNodes(t *testing.T) {
	plan := mustParse(t, hashJoinSortJSON)

	collector := &collectingRule{}
	New(collector).Analyze(plan)

	// hash_join_sort.json has 5 nodes: Sort, Hash Join, Seq Scan(orders), Hash, Seq Scan(customers)
	want := []string{"Sort", "Hash Join", "Seq Scan", "Hash", "Seq Scan"}
	if len(collector.visited) != len(want) {
		t.Fatalf("walker visited %d nodes, want %d: %v", len(collector.visited), len(want), collector.visited)
	}
	for i, got := range collector.visited {
		if got != want[i] {
			t.Errorf("visited[%d] = %q, want %q", i, got, want[i])
		}
	}
}

func TestAnalyze_FindingsCarryCorrectNodeID(t *testing.T) {
	// hash_join_sort.json: two Seq Scans — node IDs 3 (orders) and 5 (customers)
	plan := mustParse(t, hashJoinSortJSON)
	findings := New(seqScanRule{}).Analyze(plan)

	if len(findings) != 2 {
		t.Fatalf("expected 2 findings (one per Seq Scan), got %d", len(findings))
	}

	wantIDs := []int{3, 5}
	for i, f := range findings {
		if f.NodeID != wantIDs[i] {
			t.Errorf("findings[%d].NodeID = %d, want %d", i, f.NodeID, wantIDs[i])
		}
		if f.NodeType != parser.NodeSeqScan {
			t.Errorf("findings[%d].NodeType = %q, want %q", i, f.NodeType, parser.NodeSeqScan)
		}
		if f.Severity != Warn {
			t.Errorf("findings[%d].Severity = %v, want Warn", i, f.Severity)
		}
	}
}

func TestAnalyze_NodeByID_RoundTrip(t *testing.T) {
	// Verify that NodeID in a finding can be used to retrieve the originating node.
	plan := mustParse(t, hashJoinSortJSON)
	findings := New(seqScanRule{}).Analyze(plan)

	for _, f := range findings {
		node, ok := plan.NodeByID(f.NodeID)
		if !ok {
			t.Errorf("NodeByID(%d) = not found, want found", f.NodeID)
			continue
		}
		if node.NodeType != f.NodeType {
			t.Errorf("NodeByID(%d).NodeType = %q, want %q", f.NodeID, node.NodeType, f.NodeType)
		}
	}
}

func TestAnalyze_MultipleRules(t *testing.T) {
	// Two rules both fire: findings from both should be collected.
	plan := mustParse(t, seqScanJSON)

	rule1 := ruleFunc(func(n parser.Node) []Finding {
		if n.NodeType != parser.NodeSeqScan {
			return nil
		}
		return []Finding{{NodeID: n.ID, NodeType: n.NodeType, Severity: Info, Message: "rule1"}}
	})
	rule2 := ruleFunc(func(n parser.Node) []Finding {
		if n.NodeType != parser.NodeSeqScan {
			return nil
		}
		return []Finding{{NodeID: n.ID, NodeType: n.NodeType, Severity: Warn, Message: "rule2"}}
	})

	findings := New(rule1, rule2).Analyze(plan)
	if len(findings) != 2 {
		t.Fatalf("expected 2 findings (one per rule), got %d", len(findings))
	}
	if findings[0].Message != "rule1" {
		t.Errorf("findings[0].Message = %q, want %q", findings[0].Message, "rule1")
	}
	if findings[1].Message != "rule2" {
		t.Errorf("findings[1].Message = %q, want %q", findings[1].Message, "rule2")
	}
}

func TestSeverity_String(t *testing.T) {
	tests := []struct {
		sev  Severity
		want string
	}{
		{Info, "INFO"},
		{Warn, "WARN"},
		{Error, "ERROR"},
		{Severity(99), "UNKNOWN"},
	}
	for _, tt := range tests {
		if got := tt.sev.String(); got != tt.want {
			t.Errorf("Severity(%d).String() = %q, want %q", tt.sev, got, tt.want)
		}
	}
}

// ruleFunc is an adapter to use a plain function as a Rule.
type ruleFunc func(parser.Node) []Finding

func (f ruleFunc) Check(node parser.Node) []Finding { return f(node) }

// Inline JSON fixtures so advisor tests have no dependency on testdata files.
// These are the same plans as testdata/ but embedded directly for package isolation.
var seqScanJSON = []byte(`[
  {
    "Plan": {
      "Node Type": "Seq Scan",
      "Parallel Aware": false,
      "Relation Name": "orders",
      "Alias": "orders",
      "Startup Cost": 0.00,
      "Total Cost": 1849.00,
      "Plan Rows": 15,
      "Plan Width": 72,
      "Actual Startup Time": 0.042,
      "Actual Total Time": 18.721,
      "Actual Rows": 12,
      "Actual Loops": 1,
      "Filter": "(customer_id = 42)",
      "Rows Removed by Filter": 99988
    },
    "Planning Time": 0.123,
    "Execution Time": 18.854
  }
]`)

var hashJoinSortJSON = []byte(`[
  {
    "Plan": {
      "Node Type": "Sort",
      "Parallel Aware": false,
      "Startup Cost": 3120.44,
      "Total Cost": 3124.21,
      "Plan Rows": 1509,
      "Plan Width": 104,
      "Actual Startup Time": 45.123,
      "Actual Total Time": 45.891,
      "Actual Rows": 1842,
      "Actual Loops": 1,
      "Sort Key": ["o.created_at"],
      "Sort Method": "quicksort",
      "Sort Space Used": 412,
      "Sort Space Type": "Memory",
      "Plans": [
        {
          "Node Type": "Hash Join",
          "Parent Relationship": "Outer",
          "Parallel Aware": false,
          "Startup Cost": 31.50,
          "Total Cost": 3025.10,
          "Plan Rows": 1509,
          "Plan Width": 104,
          "Actual Startup Time": 2.451,
          "Actual Total Time": 38.234,
          "Actual Rows": 1842,
          "Actual Loops": 1,
          "Hash Cond": "(o.customer_id = c.id)",
          "Plans": [
            {
              "Node Type": "Seq Scan",
              "Parent Relationship": "Outer",
              "Parallel Aware": false,
              "Relation Name": "orders",
              "Alias": "o",
              "Startup Cost": 0.00,
              "Total Cost": 2846.00,
              "Plan Rows": 100000,
              "Plan Width": 72,
              "Actual Startup Time": 0.012,
              "Actual Total Time": 22.100,
              "Actual Rows": 100000,
              "Actual Loops": 1
            },
            {
              "Node Type": "Hash",
              "Parent Relationship": "Inner",
              "Parallel Aware": false,
              "Startup Cost": 25.00,
              "Total Cost": 25.00,
              "Plan Rows": 520,
              "Plan Width": 36,
              "Actual Startup Time": 2.431,
              "Actual Total Time": 2.431,
              "Actual Rows": 523,
              "Actual Loops": 1,
              "Hash Buckets": 1024,
              "Hash Batches": 1,
              "Peak Memory Usage": 32,
              "Plans": [
                {
                  "Node Type": "Seq Scan",
                  "Parent Relationship": "Outer",
                  "Parallel Aware": false,
                  "Relation Name": "customers",
                  "Alias": "c",
                  "Startup Cost": 0.00,
                  "Total Cost": 25.00,
                  "Plan Rows": 520,
                  "Plan Width": 36,
                  "Actual Startup Time": 0.009,
                  "Actual Total Time": 0.321,
                  "Actual Rows": 523,
                  "Actual Loops": 1,
                  "Filter": "(region = 'EU')",
                  "Rows Removed by Filter": 977
                }
              ]
            }
          ]
        }
      ]
    },
    "Planning Time": 0.892,
    "Execution Time": 46.203
  }
]`)
