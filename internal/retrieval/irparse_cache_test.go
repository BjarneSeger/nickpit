package retrieval

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/dgrieser/nickpit/internal/retrieval/tsparser"
)

func TestParseFileIRServesRepeatedParsesFromCache(t *testing.T) {
	src := []byte("def handler(payload):\n    return payload\n")

	first, err := parseFileIR("mod.py", src)
	if err != nil {
		t.Fatalf("parseFileIR: %v", err)
	}
	second, err := parseFileIR("mod.py", src)
	if err != nil {
		t.Fatalf("parseFileIR: %v", err)
	}
	if first != second {
		t.Fatal("identical path and content parsed twice")
	}
}

// The key includes the content hash: a rewritten file must not answer from the
// previous parse, or a review would reason about the pre-edit sources.
func TestParseFileIRReparsesChangedContent(t *testing.T) {
	first, err := parseFileIR("mod.py", []byte("def a():\n    return 1\n"))
	if err != nil {
		t.Fatalf("parseFileIR: %v", err)
	}
	second, err := parseFileIR("mod.py", []byte("def b():\n    return 2\n"))
	if err != nil {
		t.Fatalf("parseFileIR: %v", err)
	}
	if first == second {
		t.Fatal("changed content served the stale parse")
	}
	if got := second.Symbols[0].Name; got != "b" {
		t.Fatalf("symbol %q, want b", got)
	}
}

// The same path with the same bytes must never be parsed twice at the same
// time: concurrent lanes asking for one large file is exactly the OOM this
// cache exists to prevent, so all callers have to land on one entry.
func TestParseFileIRSingleFlightsConcurrentCallers(t *testing.T) {
	src := []byte("def concurrent_handler(payload):\n    return payload\n")
	const callers = 8

	results := make([]*tsparser.FileIR, callers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ir, err := parseFileIR("concurrent.py", src)
			if err != nil {
				t.Error(err)
				return
			}
			results[i] = ir
		}()
	}
	close(start)
	wg.Wait()

	for i, ir := range results {
		if ir == nil {
			t.Fatalf("caller %d got no IR", i)
		}
		if ir != results[0] {
			t.Fatalf("caller %d got its own parse; the single-flight did not hold", i)
		}
	}
}

func TestFileIRCacheEvictsLeastRecentlyUsed(t *testing.T) {
	t.Setenv("NICKPIT_IR_CACHE_MAX_ENTRIES", "2")
	cache := referenceCacheStore[fileIRCacheEntry]{
		capEnv:      "NICKPIT_IR_CACHE_MAX_ENTRIES",
		capFallback: 2,
	}
	for i := range 5 {
		cache.entry(fmt.Sprintf("f%d.py", i))
	}
	if len(cache.entries) != 2 {
		t.Fatalf("cache holds %d entries, want 2", len(cache.entries))
	}
}

// The IR cache holds thousands of files while the reference caches hold two
// repository roots, so one cap must not bound the other.
func TestCacheCapsAreIndependent(t *testing.T) {
	t.Setenv("NICKPIT_REFERENCE_CACHE_MAX_ENTRIES", "1")
	t.Setenv("NICKPIT_IR_CACHE_MAX_ENTRIES", "4")

	irCache := referenceCacheStore[fileIRCacheEntry]{capEnv: "NICKPIT_IR_CACHE_MAX_ENTRIES", capFallback: 1}
	var refCache referenceCacheStore[parsedReferenceCacheEntry]
	for i := range 4 {
		irCache.entry(strconv.Itoa(i))
		refCache.entry(strconv.Itoa(i))
	}
	if len(irCache.entries) != 4 {
		t.Fatalf("IR cache holds %d entries, want 4", len(irCache.entries))
	}
	if len(refCache.entries) != 1 {
		t.Fatalf("reference cache holds %d roots, want 1", len(refCache.entries))
	}
}

// An oversize file must reach the model as "not analyzed", never as "symbol
// absent": the model would otherwise conclude the code does not exist.
func TestUnparsedFileReasonReportsSizeSkip(t *testing.T) {
	repoRoot := t.TempDir()
	var b strings.Builder
	for i := range 200 {
		fmt.Fprintf(&b, "def handler_%d(payload):\n    return {\"a\": payload[\"u\"]}\n\n", i)
	}
	writeRetrievalFile(t, repoRoot, "big.py", b.String())
	t.Setenv("NICKPIT_MAX_STRUCTURAL_PARSE_BYTES", "64")

	reason := unparsedFileReason(repoRoot, "big.py")
	if reason == "" {
		t.Fatal("size-skipped file reported no reason")
	}
	if !strings.Contains(reason, "exceeds") {
		t.Fatalf("reason %q does not explain the skip", reason)
	}
}

func TestUnparsedFileReasonEmptyForParsedFile(t *testing.T) {
	repoRoot := t.TempDir()
	writeRetrievalFile(t, repoRoot, "small.py", "def handler(payload):\n    return payload\n")

	if reason := unparsedFileReason(repoRoot, "small.py"); reason != "" {
		t.Fatalf("parsed file reported reason %q", reason)
	}
}

// A lookup that lands on a size-skipped file must say the analysis never ran.
// Reporting a bare "not found" would let a reviewer conclude the symbol does
// not exist, which is exactly the wrong inference for a file nobody parsed.
func TestFindCallersOnSizeSkippedFileExplainsTheSkip(t *testing.T) {
	repoRoot := t.TempDir()
	var b strings.Builder
	for i := range 200 {
		fmt.Fprintf(&b, "def handler_%d(payload):\n    return {\"a\": payload[\"u\"]}\n\n", i)
	}
	writeRetrievalFile(t, repoRoot, "bot.py", b.String())
	t.Setenv("NICKPIT_MAX_STRUCTURAL_PARSE_BYTES", "64")

	engine := NewLocalEngine()
	_, err := engine.FindCallers(context.Background(), repoRoot, SymbolRef{Name: "handler_7", Path: "bot.py"}, 1)
	if err == nil {
		t.Fatal("expected a lookup failure for an unparsed file")
	}
	if !strings.Contains(err.Error(), "unparsed") {
		t.Fatalf("error %q does not disclose that the file was left unparsed", err)
	}
	if !strings.Contains(err.Error(), "literal search") {
		t.Fatalf("error %q does not point at the fallback", err)
	}
}
