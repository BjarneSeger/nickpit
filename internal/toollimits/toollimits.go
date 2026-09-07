// Package toollimits holds the limits and defaults that define what the agent
// tools accept and return: schemas, execution and result pruning all read them
// from here so they cannot drift apart.
//
// It deliberately imports nothing. The catalog in internal/tools describes the
// tools to the model and therefore depends on internal/llm; keeping the numbers
// separate lets low-level packages — git plumbing, config loading, retrieval —
// honor the same limits without taking a dependency on the LLM layer.
//
// Limits that are not part of any tool's contract stay with the code that
// enforces them: internal/git owns its patch-byte, deepen and ambiguity caps,
// which bound git command output rather than a tool's request or response.
// Token limits are context-dependent and live in the review/config layers.
package toollimits

const (
	DefaultListFilesDepth      = 1
	DefaultSearchContextLines  = 5
	MaxSearchStructuralLookups = 20
	// MaxFallbackSearchResults bounds the literal searches the engine runs on
	// its own behalf when a structural lookup degrades — a common identifier
	// would otherwise stream every match in the repository into the result.
	MaxFallbackSearchResults = 100
	// MaxOpportunisticGoLoadFiles is the largest Go file count an opportunistic
	// lookup (one that set AvoidGoLoad) may trigger a whole-repository
	// type-check for. Above it the snapshot is used only when already cached:
	// packages.Load over a monorepo takes minutes, which an explicit
	// find_references may spend but a rewritten literal search must not.
	MaxOpportunisticGoLoadFiles  = 500
	MaxFindLinesMatches          = 100
	DefaultCallHierarchyDepth    = 10
	MaxCallHierarchyDepth        = 50
	MaxReferenceFunctions        = 25
	MaxAmbiguousReferenceTargets = 10
	MaxRetrievedFileBytes        = 5 << 20

	// MaxTreeSitterParseBytes caps the source one tree-sitter parse may see.
	// The pure-Go runtime (gotreesitter, used for Python and Rust) allocates
	// ~0.45 MB of heap per KB of source, linearly: a measured 1 MB Python file
	// peaks at 439 MB and takes 1.8 s, so 1 MiB bounds one parse at roughly
	// half a GB. That is well above every realistic source file — a repository
	// this size is an outlier at the 99th percentile — while still refusing
	// the multi-MB inputs MaxRetrievedFileBytes would otherwise admit, which
	// scale straight into the gigabytes and can take the whole process with
	// them. Above the cap the file is left unparsed and callers degrade to
	// literal search rather than claiming a symbol is absent.
	// NICKPIT_MAX_STRUCTURAL_PARSE_BYTES tunes it, a value <= 0 disables the
	// cap (the pre-cap behavior).
	//
	// The numbers above are gotreesitter v0.52. Up to v0.21 the same parse
	// cost 5-10 MB per KB and grew superlinearly (a 229 KB file peaked at
	// 1.8 GB over 20 s, and ~840 MB of it survived an explicit GC inside the
	// library's parser pool), which is what made the review daemon OOM.
	MaxTreeSitterParseBytes = 1 << 20

	// DefaultFileIRCacheEntries bounds how many parsed files the IR cache
	// keeps. One entry retains a file's symbols including their source text,
	// so this is sized to hold a normal repository's parse results (tens of
	// MB) rather than a monorepo's. Eviction only costs a re-parse.
	// NICKPIT_IR_CACHE_MAX_ENTRIES tunes it; a value <= 0 disables eviction.
	DefaultFileIRCacheEntries = 1024

	// DefaultStaticGraphCacheEntries bounds how many distinct (language,
	// repoRoot, scope) call graphs one run memoizes.
	DefaultStaticGraphCacheEntries = 64
	// DefaultReferenceCacheEntries counts repository roots, and applies to the
	// parsed-source snapshot and the type-checked Go snapshot separately, so
	// two roots can retain up to two of each. Each snapshot holds a whole
	// repository, so this stays small: one root covers a review, the second is
	// headroom for a concurrent one.
	DefaultReferenceCacheEntries = 2

	DefaultGitLogLimit    = 20
	MaxGitLogLimit        = 200
	DefaultGitShowCommits = 10
	MaxGitShowCommits     = 50

	// DefaultMaxToolCalls is 0, meaning unlimited calls per agent.
	DefaultMaxToolCalls = 0
	// DefaultMaxDuplicateToolCalls cuts an agent over to its final no-tools call
	// after this many repeated requests. Kept low because a repeat buys nothing
	// — the loop answers it with already_requested — while still costing a whole
	// turn of generation, which is what a review's wall-clock time is made of.
	DefaultMaxDuplicateToolCalls = 2
)
