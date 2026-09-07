package retrieval

import (
	"crypto/sha256"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/dgrieser/nickpit/internal/retrieval/repofs"
	"github.com/dgrieser/nickpit/internal/retrieval/tsparser"
	"github.com/dgrieser/nickpit/internal/toollimits"
)

// fileIRCacheEntry memoizes one file's parse. The sync.Once is the
// single-flight: the reviewer lanes run concurrently and repeatedly ask for the
// same files (a repo-wide graph, a directory-scoped graph and a reference
// lookup all cover the same sources), and one tree-sitter parse of a large file
// costs hundreds of MB, so a second concurrent parse of the same bytes is the
// difference between a review and an OOM kill.
type fileIRCacheEntry struct {
	once sync.Once
	ir   *tsparser.FileIR
	err  error
}

// fileIRCache is keyed by path plus content hash, so a rewritten file parses
// again while an unchanged one never does — including across the graph and
// reference caches, which key by repository root and scope and therefore each
// used to pay their own parse of every file.
var fileIRCache = referenceCacheStore[fileIRCacheEntry]{
	capEnv:      "NICKPIT_IR_CACHE_MAX_ENTRIES",
	capFallback: toollimits.DefaultFileIRCacheEntries,
}

// parseFileIR returns the IR for src, parsing it at most once per (path,
// content) in this process and never twice at the same time. Eviction can drop
// an entry a caller still holds; that only costs a later re-parse and can never
// produce a different answer, because the key pins the exact bytes.
func parseFileIR(path string, src []byte) (*tsparser.FileIR, error) {
	sum := sha256.Sum256(src)
	entry := fileIRCache.entry(path + "\x00" + string(sum[:]))
	entry.once.Do(func() {
		entry.ir, entry.err = tsparser.ParseFile(path, src)
	})
	return entry.ir, entry.err
}

// parseIRFiles parses files (absolute paths under repoRoot) with tsparser in
// parallel and returns the IR keyed by repo-relative slash path.
func parseIRFiles(repoRoot string, files []string) (map[string]*tsparser.FileIR, error) {
	type result struct {
		rel string
		ir  *tsparser.FileIR
		err error
	}
	jobs := make(chan string)
	results := make(chan result)
	var wg sync.WaitGroup
	workers := min(runtime.GOMAXPROCS(0), len(files))
	for range workers {
		wg.Go(func() {
			for fullPath := range jobs {
				rel, err := filepath.Rel(repoRoot, fullPath)
				if err != nil {
					results <- result{err: err}
					continue
				}
				rel = filepath.ToSlash(rel)
				data, err := repofs.ReadFile(repoRoot, fullPath)
				if err != nil {
					results <- result{err: err}
					continue
				}
				ir, err := parseFileIR(rel, data)
				results <- result{rel: rel, ir: ir, err: err}
			}
		})
	}
	go func() {
		for _, fullPath := range files {
			jobs <- fullPath
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	out := make(map[string]*tsparser.FileIR, len(files))
	var firstErr error
	for res := range results {
		if res.err != nil {
			if firstErr == nil {
				firstErr = res.err
			}
			continue
		}
		out[res.rel] = res.ir
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// unparsedFileReason reports why path has no structural symbols when the cause
// is a parse the retrieval layer declined (currently: the file exceeds the
// tree-sitter parse cap), and "" for every other case — including a file that
// parsed fine, an unreadable file and a language with no tsparser backend. The
// parse is served from fileIRCache, so this costs a file read and a hash.
func unparsedFileReason(repoRoot, path string) string {
	if path == "" {
		return ""
	}
	data, err := repofs.ReadFile(repoRoot, filepath.Join(repoRoot, filepath.FromSlash(path)))
	if err != nil {
		return ""
	}
	ir, err := parseFileIR(path, data)
	if err != nil || ir == nil {
		return ""
	}
	return ir.UnparsedReason
}

// sortSymbolInfos orders symbol results by path, then start line.
func sortSymbolInfos(out []*SymbolInfo) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path == out[j].Path {
			return out[i].StartLine < out[j].StartLine
		}
		return out[i].Path < out[j].Path
	})
}

// symbolsNamed returns SymbolInfo entries for every graph node with the given
// name whose path falls inside scopePath ("" = everywhere). Used to answer
// symbol lookups from the cached call graph instead of re-parsing the tree.
func (g *staticGraph) symbolsNamed(name, scopePath string) []*SymbolInfo {
	var out []*SymbolInfo
	for _, id := range g.byName[name] {
		node := g.nodes[id]
		if scopePath != "" && node.Path != scopePath && !strings.HasPrefix(node.Path, scopePath+"/") {
			continue
		}
		out = append(out, &SymbolInfo{
			Name:      node.Name,
			Path:      node.Path,
			StartLine: node.StartLine,
			EndLine:   node.EndLine,
			Source:    node.Source,
			Language:  g.language,
		})
	}
	sortSymbolInfos(out)
	return out
}
