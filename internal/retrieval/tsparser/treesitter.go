package tsparser

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"

	"github.com/dgrieser/nickpit/internal/toollimits"
)

// ErrSourceTooLarge reports that a file was left unparsed because its size
// exceeds the tree-sitter parse cap. It is a budget decision, never evidence
// about the file's content: callers must degrade to literal inspection rather
// than treat the missing symbols as absence. See
// toollimits.MaxTreeSitterParseBytes for the cost this bounds.
var ErrSourceTooLarge = errors.New("source exceeds the tree-sitter parse size cap")

// tsParse parses src with the grammar registered for the canonical filename
// (grammar lookup is extension-based; callers pass "x.py"/"x.rs"). It uses the
// library's per-language parser pool, which is safe for concurrent use.
// The caller must Release() the returned tree.
//
// Oversize input returns ErrSourceTooLarge without parsing: the pure-Go
// runtime's heap cost grows superlinearly with source size and one large file
// can exhaust the whole container.
func tsParse(canonicalName string, src []byte) (*sitter.BoundTree, error) {
	if limit := parseByteCap(); limit > 0 && len(src) > limit {
		return nil, fmt.Errorf("%w: %d bytes exceeds the %d byte cap", ErrSourceTooLarge, len(src), limit)
	}
	return grammars.ParseFilePooled(canonicalName, src)
}

// parseByteCap resolves the parse size cap. Reading the environment at the
// point of use mirrors the retrieval caches' cap knobs;
// NICKPIT_MAX_STRUCTURAL_PARSE_BYTES lets an operator with a larger memory
// budget raise it, and a value <= 0 disables the cap entirely.
func parseByteCap() int {
	raw := strings.TrimSpace(os.Getenv("NICKPIT_MAX_STRUCTURAL_PARSE_BYTES"))
	if raw == "" {
		return toollimits.MaxTreeSitterParseBytes
	}
	limit, err := strconv.Atoi(raw)
	if err != nil {
		return toollimits.MaxTreeSitterParseBytes
	}
	return limit
}

// namedChildren returns the named children of n (nil-safe, like field).
func namedChildren(n *sitter.Node) []*sitter.Node {
	if n == nil {
		return nil
	}
	count := n.NamedChildCount()
	out := make([]*sitter.Node, 0, count)
	for i := range count {
		out = append(out, n.NamedChild(i))
	}
	return out
}

// field returns the child of n for the named grammar field, or nil. Unlike
// the BoundTree accessors, Node.ChildByFieldName is not nil-safe, so guard
// here once for every call site.
func field(bt *sitter.BoundTree, n *sitter.Node, name string) *sitter.Node {
	if n == nil {
		return nil
	}
	return n.ChildByFieldName(name, bt.Language())
}

// subtreeHasError reports whether n's subtree contains an ERROR or MISSING
// node. Node.HasError alone is not enough: the runtime does not propagate
// MISSING (recovered) tokens into the ancestor error flag.
func subtreeHasError(n *sitter.Node) bool {
	if n == nil {
		return false
	}
	found := false
	sitter.Walk(n, func(node *sitter.Node, _ int) sitter.WalkAction {
		if node.IsError() || node.IsMissing() {
			found = true
			return sitter.WalkStop
		}
		return sitter.WalkContinue
	})
	return found
}
