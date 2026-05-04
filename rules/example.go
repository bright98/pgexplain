// Package rules contains the rule implementations for the pgexplain advisor.
// Each file in this package implements one rule. Add new rules by creating
// a new file and implementing the advisor.Rule interface.
package rules

import (
	"github.com/pgexplain/pgexplain/advisor"
	"github.com/pgexplain/pgexplain/parser"
)

// ExampleRule is a no-op rule that always returns empty findings.
// It exists only to demonstrate how to implement the advisor.Rule interface.
// Replace this with real rules in Phase 3.
type ExampleRule struct{}

func (ExampleRule) Check(_ parser.Node) []advisor.Finding { return nil }
