package tsparser

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func pythonSource(functions int) []byte {
	var b strings.Builder
	for i := range functions {
		fmt.Fprintf(&b, "def handler_%d(payload):\n    return {\"a\": payload[\"u\"], \"b\": [1, 2]}\n\n", i)
	}
	return []byte(b.String())
}

// The cap exists because one oversize tree-sitter parse can exhaust the whole
// container, so it must hold for every tree-sitter language and leave the file
// marked as unparsed rather than as empty.
func TestParseFileSkipsOversizeSource(t *testing.T) {
	src := pythonSource(200)
	t.Setenv("NICKPIT_MAX_STRUCTURAL_PARSE_BYTES", strconv.Itoa(len(src)-1))

	ir, err := ParseFile("big.py", src)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if !ir.Unparsed {
		t.Fatal("oversize file was not marked unparsed")
	}
	if !ir.HasError {
		t.Fatal("oversize file must degrade to low confidence like any unparsed file")
	}
	if len(ir.Symbols) != 0 {
		t.Fatalf("oversize file reported %d symbols", len(ir.Symbols))
	}
	if !strings.Contains(ir.UnparsedReason, "exceeds") {
		t.Fatalf("reason %q does not explain the skip", ir.UnparsedReason)
	}
	if !errors.Is(ErrSourceTooLarge, ErrSourceTooLarge) {
		t.Fatal("sentinel is not comparable")
	}
}

func TestParseFileParsesWithinCap(t *testing.T) {
	src := pythonSource(20)
	t.Setenv("NICKPIT_MAX_STRUCTURAL_PARSE_BYTES", strconv.Itoa(len(src)+1))

	ir, err := ParseFile("small.py", src)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if ir.Unparsed {
		t.Fatalf("file within the cap was skipped: %s", ir.UnparsedReason)
	}
	if len(ir.Symbols) != 20 {
		t.Fatalf("parsed %d symbols, want 20", len(ir.Symbols))
	}
}

func TestParseCapDisabledByZero(t *testing.T) {
	src := pythonSource(20)
	t.Setenv("NICKPIT_MAX_STRUCTURAL_PARSE_BYTES", "0")

	ir, err := ParseFile("small.py", src)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if ir.Unparsed {
		t.Fatal("a cap of 0 must disable the check, not skip everything")
	}
}

func TestParseCapAppliesToRust(t *testing.T) {
	src := []byte("fn main() {\n    println!(\"hi\");\n}\n")
	t.Setenv("NICKPIT_MAX_STRUCTURAL_PARSE_BYTES", "1")

	ir, err := ParseFile("main.rs", src)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if !ir.Unparsed {
		t.Fatal("oversize Rust file was not marked unparsed")
	}
}

// The esbuild-based JS/TS family does not have the tree-sitter runtime's
// allocation profile, so the cap must not silently degrade it.
func TestParseCapDoesNotApplyToTypeScript(t *testing.T) {
	src := []byte("export function handler(a: number): number {\n  return a + 1;\n}\n")
	t.Setenv("NICKPIT_MAX_STRUCTURAL_PARSE_BYTES", "1")

	ir, err := ParseFile("handler.ts", src)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if ir.Unparsed {
		t.Fatalf("TypeScript file was skipped by the tree-sitter cap: %s", ir.UnparsedReason)
	}
	if len(ir.Symbols) != 1 {
		t.Fatalf("parsed %d symbols, want 1", len(ir.Symbols))
	}
}
