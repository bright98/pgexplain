// Command pgexplain analyzes PostgreSQL EXPLAIN (ANALYZE, FORMAT JSON) output
// and emits human-readable performance findings.
//
// Usage:
//
//	pgexplain [flags] [plan.json]
//	<command> | pgexplain [flags]
//
// If no file argument is given, pgexplain reads from stdin.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Bright98/pgexplain/advisor"
	"github.com/Bright98/pgexplain/parser"
	"github.com/Bright98/pgexplain/rules"
)

// valueColumn is the character position where field values start in text output.
// It equals the width of the widest label prefix: "  suggestion: " = 14.
const valueColumn = 14

func main() {
	format := flag.String("format", "text", `Output format: "text" (default) or "json"`)
	flag.Usage = usage
	flag.Parse()

	data, err := readInput(flag.Args())
	if err != nil {
		fmt.Fprintf(os.Stderr, "pgexplain: %v\n", err)
		os.Exit(2)
	}

	plan, err := parser.Parse(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pgexplain: %v\n", err)
		os.Exit(2)
	}

	findings := buildAdvisor().Analyze(plan)

	switch *format {
	case "json":
		printJSON(findings)
	default:
		printText(findings, plan)
	}

	os.Exit(exitCode(findings))
}

// readInput returns the raw EXPLAIN JSON from stdin or the first file argument.
func readInput(args []string) ([]byte, error) {
	if len(args) == 0 {
		return io.ReadAll(os.Stdin)
	}
	f, err := os.Open(args[0])
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// buildAdvisor creates an Advisor with all built-in rules at their defaults.
func buildAdvisor() *advisor.Advisor {
	return advisor.New(
		rules.SeqScan(),
		rules.RowEstimateMismatch(),
		rules.HashJoinSpill(),
		rules.NestedLoopLarge(),
		rules.MissingIndexOnlyScan(),
		rules.SortSpill(),
		rules.TopNHeapsort(),
		rules.ParallelNotLaunched(),
	)
}

// exitCode returns 0 if all findings are Info (or there are none),
// 1 if any finding is Warn or Error, mirroring standard CI gate conventions.
func exitCode(findings []advisor.Finding) int {
	for _, f := range findings {
		if f.Severity >= advisor.Warn {
			return 1
		}
	}
	return 0
}

// --- Text output ---------------------------------------------------------

func printText(findings []advisor.Finding, plan *parser.Plan) {
	if len(findings) == 0 {
		fmt.Println("No findings.")
		return
	}

	for i, f := range findings {
		if i > 0 {
			fmt.Println()
		}
		node, _ := plan.NodeByID(f.NodeID)
		fmt.Printf("%-8s%s\n", "["+severityLabel(f.Severity)+"]", f.Message)
		printField("node", fmt.Sprintf("%s (ID %d)", node.NodeType, node.ID))
		printField("detail", f.Detail)
		printField("suggestion", f.Suggestion)
	}

	fmt.Println()
	fmt.Println(summaryLine(findings))
}

// printField prints "  label:  value" with continuation lines indented to
// valueColumn so that multi-line values (detail, suggestion) stay aligned.
func printField(label, value string) {
	prefix := fmt.Sprintf("  %s: ", label)
	for len(prefix) < valueColumn {
		prefix += " "
	}
	continuation := strings.Repeat(" ", valueColumn)
	indented := strings.ReplaceAll(value, "\n", "\n"+continuation)
	fmt.Printf("%s%s\n", prefix, indented)
}

// severityLabel returns the severity word used inside brackets in text output.
func severityLabel(s advisor.Severity) string {
	switch s {
	case advisor.Error:
		return "ERROR"
	case advisor.Warn:
		return "WARN"
	case advisor.Info:
		return "INFO"
	default:
		return "UNKNOWN"
	}
}

// summaryLine returns a one-line count of findings by severity.
func summaryLine(findings []advisor.Finding) string {
	var errors, warnings, infos int
	for _, f := range findings {
		switch f.Severity {
		case advisor.Error:
			errors++
		case advisor.Warn:
			warnings++
		case advisor.Info:
			infos++
		}
	}
	n := len(findings)
	noun := "finding"
	if n != 1 {
		noun = "findings"
	}
	return fmt.Sprintf("%d %s: %d error(s), %d warning(s), %d info", n, noun, errors, warnings, infos)
}

// --- JSON output ---------------------------------------------------------

type jsonFinding struct {
	Severity   string `json:"severity"`
	NodeID     int    `json:"node_id"`
	NodeType   string `json:"node_type"`
	Message    string `json:"message"`
	Detail     string `json:"detail"`
	Suggestion string `json:"suggestion"`
}

func printJSON(findings []advisor.Finding) {
	out := make([]jsonFinding, len(findings))
	for i, f := range findings {
		out[i] = jsonFinding{
			Severity:   severityString(f.Severity),
			NodeID:     f.NodeID,
			NodeType:   f.NodeType,
			Message:    f.Message,
			Detail:     f.Detail,
			Suggestion: f.Suggestion,
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}

func severityString(s advisor.Severity) string {
	switch s {
	case advisor.Error:
		return "ERROR"
	case advisor.Warn:
		return "WARN"
	case advisor.Info:
		return "INFO"
	default:
		return "UNKNOWN"
	}
}

// --- Usage ---------------------------------------------------------------

func usage() {
	fmt.Fprint(os.Stderr, `pgexplain — analyze PostgreSQL EXPLAIN (ANALYZE, FORMAT JSON) output

Usage:
  pgexplain [flags] [plan.json]
  <command> | pgexplain [flags]

If no file is given, pgexplain reads EXPLAIN JSON from stdin.

Flags:
`)
	flag.PrintDefaults()
	fmt.Fprint(os.Stderr, `
Exit codes:
  0  No findings, or only Info findings
  1  At least one Warn or Error finding
  2  Invalid input or parse error

Examples:
  # Pipe directly from psql
  psql -U myuser -d mydb -c "EXPLAIN (ANALYZE, FORMAT JSON) SELECT * FROM orders WHERE customer_id = 42" | pgexplain

  # Read from a saved plan file
  pgexplain plan.json

  # Machine-readable output for CI tooling
  pgexplain --format=json plan.json

  # Use exit code in a CI gate
  pgexplain plan.json || echo "plan has performance issues"
`)
}
