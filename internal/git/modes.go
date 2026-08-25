package git

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/dgrieser/nickpit/internal/model"
)

// maxTreeQueryPaths caps how many pathspecs one "ls-tree" call receives so a
// large change set cannot overflow the command line.
const maxTreeQueryPaths = 100

// literalPathspec wraps a path so git takes it verbatim. "--" stops option
// parsing but not pathspec magic, and these paths come from SCM payloads: a file
// literally named ":(literal)link" would otherwise be reparsed as magic (missing
// the real file), and one named ":!foo" would turn into an exclusion that
// enumerates the rest of the tree.
func literalPathspec(path string) string {
	return ":(literal)" + path
}

// topLiteralPathspec is literalPathspec anchored at the repo top level, for
// commands that have no equivalent of ls-tree's --full-tree. Without "top" a
// pathspec is resolved against the runner's working directory, so a checkout root
// below the top level would match nothing and the change would come back unmarked.
func topLiteralPathspec(path string) string {
	return ":(top,literal)" + path
}

// SymlinkPathsAtRev asks git which of paths are stored as symlinks (mode 120000)
// in rev's tree. The result maps each such path, as git reports it, to the object
// name of its blob — a symlink's blob is its target, so that name is what makes
// the target readable when the patch shows no content of its own.
//
// The tree of the reviewed commit — not the index and not the worktree — is the
// authority, for two independent reasons. A checkout can hold a different
// revision than the diff describes (a user-selected repo root for a remote
// review, staged content, a range that is not checked out), and with
// core.symlinks=false (the default on Windows, and a valid setting on Unix) git
// materializes a symlink blob as a regular file holding the target path, so an
// lstat would report a plain file. Addressing the tree by SHA sidesteps both: a
// checkout that does not have that commit simply fails the lookup.
//
// Paths absent from the tree (a deletion, an untracked file) are absent from the
// result, and a failing git call yields no marks rather than a guess: a missing
// mark, never a wrong one. The error is returned so a caller that can log it may,
// but it never invalidates the marks already collected.
func SymlinkPathsAtRev(ctx context.Context, runner Runner, rev string, paths []string) (map[string]string, error) {
	if runner == nil || rev == "" || len(paths) == 0 {
		return nil, nil
	}
	symlinks := make(map[string]string, len(paths))
	var firstErr error
	for chunk := range slices.Chunk(paths, maxTreeQueryPaths) {
		args := make([]string, 0, 5+len(chunk))
		// --full-tree makes git act as though it ran from the repo top level.
		// Without it both the pathspecs and the printed paths are resolved
		// relative to the runner's working directory, so every lookup silently
		// matches nothing whenever that directory is not the top level — a
		// --repo-root pointing at a subdirectory, for instance.
		args = append(args, "ls-tree", "-z", "--full-tree", rev, "--")
		for _, path := range chunk {
			args = append(args, literalPathspec(path))
		}
		out, err := runner.Run(ctx, args...)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		collectTreeSymlinks(out, symlinks)
	}
	return symlinks, firstErr
}

// StableFileModes reports the file mode of each of paths, but only for the paths
// whose mode is the SAME in every side of every entry the given commits show for
// them. Paths the commits do not touch, and paths whose mode changes anywhere
// inside that range, are absent from the result. No blob names are reported: a
// deletion's surviving blob is the pre-image one, which this listing's destination
// column does not carry.
//
// This answers the question a deleted path raises. It is absent from the reviewed
// tree, so SymlinkPathsAtRev can say nothing about it — yet the removed side of
// the patch is exactly where a removed symlink's target sits, and a source that
// reports no file modes (GitHub) leaves the entry unmarked otherwise. What the
// review needs is the mode on the pre-change side of the WHOLE change, and the
// only way this listing can vouch for that is unanimity: if the path was a symlink
// at every point the range touches it, it was a symlink before the range too.
//
// Disagreement is therefore reported as nothing rather than as a pick. A path
// deleted, re-added as a regular file and deleted again — or a regular file
// typechanged into a symlink and then removed — has no single pre-change mode
// here, and choosing one would mean marking a link as text or text as a link. That
// also makes the result independent of ordering: neither git's sort of --no-walk
// output nor the order commits are chunked in can change it.
//
// The listing is restricted to the named commits with --no-walk, so nothing walks
// history: the change under review happened in its own commits, and a commit the
// checkout does not have simply fails the lookup. --no-renames keeps a deletion a
// deletion; git would otherwise pair it with an addition elsewhere and hide the
// mode this looks for.
//
// A failing call yields no modes rather than a guess. The error is returned so a
// caller that can log it may, but it never invalidates what was collected.
func StableFileModes(ctx context.Context, runner Runner, commits, paths []string) (FileModes, error) {
	if runner == nil || len(commits) == 0 || len(paths) == 0 {
		return nil, nil
	}
	// "" marks a path whose sides disagree; it is dropped from the result.
	seen := make(map[string]string, len(paths))
	var firstErr error
	for commitChunk := range slices.Chunk(commits, maxTreeQueryPaths) {
		for pathChunk := range slices.Chunk(paths, maxTreeQueryPaths) {
			args := make([]string, 0, 8+len(commitChunk)+len(pathChunk))
			// --no-relative keeps the reported paths repo-root-relative: with
			// diff.relative=true set in a user's config, a command run from a
			// subdirectory would report "link" where the change says "sub/link".
			args = append(args, "log", "--no-walk", "--format=", "--raw", "-z", "--no-relative", "--no-renames")
			args = append(args, commitChunk...)
			args = append(args, "--")
			for _, path := range pathChunk {
				args = append(args, topLiteralPathspec(path))
			}
			out, err := runner.Run(ctx, args...)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			collectStableModes(out, seen)
		}
	}
	modes := FileModes{}
	for path, mode := range seen {
		if mode != "" {
			modes[path] = RawFileEntry{Mode: mode}
		}
	}
	return modes, firstErr
}

// collectStableModes folds "git log --raw -z" entries into per-path modes, marking
// a path "" as soon as two of its stated modes differ. Absent sides ("000000") say
// nothing about the mode and are skipped; an addition and a deletion of the same
// kind of file therefore still agree.
func collectStableModes(out string, seen map[string]string) {
	tokens := strings.Split(out, "\x00")
	for i := 0; i < len(tokens); i++ {
		if !strings.HasPrefix(tokens[i], ":") {
			continue
		}
		parents, dst, status, ok := RawEntryModes(tokens[i])
		if !ok {
			continue
		}
		paths := 1
		if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
			paths = 2
		}
		if i+paths >= len(tokens) {
			return
		}
		path := tokens[i+paths]
		i += paths
		if path == "" {
			continue
		}
		for _, mode := range append(parents, dst) {
			if mode == "" {
				continue
			}
			switch current, known := seen[path]; {
			case !known:
				seen[path] = mode
			case current != mode:
				// Two different modes inside the range: nothing to vouch for.
				seen[path] = ""
			}
		}
	}
}

// collectTreeSymlinks parses "ls-tree -z" output. Each NUL-terminated entry is
// "<mode> <type> <object>\t<path>"; -z keeps the path literal, so it needs no
// unquoting.
func collectTreeSymlinks(out string, symlinks map[string]string) {
	for entry := range strings.SplitSeq(out, "\x00") {
		meta, path, ok := strings.Cut(entry, "\t")
		if !ok || path == "" {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) < 3 || NormalizeFileMode(fields[0]) != SymlinkFileMode {
			continue
		}
		symlinks[path] = fields[2]
	}
}

// ReadBlob returns an object's bytes exactly as git stores them, refusing anything
// larger than limit. Nothing is trimmed: for a symlink the blob is the target
// pathname, git appends no separator, and a pathname may legally contain — or end
// in — a newline, so any trimming would change the target.
//
// A LimitedRunner is used when the runner provides one, for two reasons that both
// matter for a value the caller treats as byte-exact: RunLimited captures stderr
// separately, so a git warning on an otherwise successful call (an unreadable
// gitattributes include, an advice notice, fetch chatter) cannot be spliced into
// the result, and it stops reading at the cap instead of buffering a whole blob
// only to reject it.
func ReadBlob(ctx context.Context, runner Runner, blob string, limit int) (string, error) {
	if runner == nil || blob == "" {
		return "", errors.New("git: no blob to read")
	}
	args := []string{"cat-file", "blob", blob}
	if limited, ok := runner.(LimitedRunner); ok && limit > 0 {
		out, truncated, err := limited.RunLimited(ctx, limit, args...)
		if err != nil {
			return "", err
		}
		if truncated {
			return "", fmt.Errorf("git: blob %s exceeds %d bytes", blob, limit)
		}
		return out, nil
	}
	// Plain-Runner fallback (test fakes, wrappers): the whole object is read and
	// the cap is applied afterwards.
	out, err := runner.Run(ctx, args...)
	if err != nil {
		return "", err
	}
	if limit > 0 && len(out) > limit {
		return "", fmt.Errorf("git: blob %s exceeds %d bytes", blob, limit)
	}
	return out, nil
}

// MaxSymlinkTargetBytes bounds what is accepted as a link target. POSIX caps a
// symlink at PATH_MAX; anything larger is not a target, so it is not read into the
// review context.
const MaxSymlinkTargetBytes = 4096

// AttachSymlinkTargets fills SymlinkTarget for symlink entries whose patch shows
// no content at all — a pure rename, where git emits only "rename from/to" lines.
// Such a change is exactly the one worth reviewing (moving a relative symlink can
// break its target) and the one an agent cannot judge from the patch, so the blob
// named by blobFor is read directly. blobFor returns "" for a path whose blob is
// unknown, which leaves that entry without a target.
//
// Entries whose patch does carry the target (an added or changed symlink) are left
// alone: the diff already shows it, and reading the blob again would only risk
// disagreeing with what the reviewer sees. An entry that already has a target is
// never re-read either.
//
// A failing read yields no target rather than a guess: the change is reviewed
// either way, and a wrong target would be worse than a missing one.
func AttachSymlinkTargets(ctx context.Context, runner Runner, files []model.ChangedFile, hunks []model.DiffHunk, blobFor func(path string) string) {
	if runner == nil || blobFor == nil {
		return
	}
	hasHunk := make(map[string]bool, len(hunks))
	for _, hunk := range hunks {
		hasHunk[hunk.FilePath] = true
	}
	for i := range files {
		file := &files[i]
		if !file.Symlink || file.SymlinkTarget != "" || hasHunk[file.Path] {
			continue
		}
		blob := blobFor(file.Path)
		if blob == "" {
			continue
		}
		// The blob IS the target, byte for byte: git appends no separator, and a
		// pathname may legally end in a newline, so nothing may be trimmed here.
		target, err := ReadBlob(ctx, runner, blob, MaxSymlinkTargetBytes)
		if err != nil {
			continue
		}
		file.SymlinkTarget = target
	}
}

// FileModes maps a repo-relative path to what "git diff --raw" reports for that
// path. It fills the gaps a patch leaves: git prints a mode header only when the
// mode is new, gone, or changed, so a plain rename of an unchanged symlink carries
// no 120000 anywhere in its patch — and no content either, which is why the blob
// name is kept alongside the mode.
type FileModes map[string]RawFileEntry

// RawFileEntry is one raw-listing entry: the file mode that describes the side
// under review, and the object name of that side's blob (empty for a deletion,
// whose blob is gone).
type RawFileEntry struct {
	Mode string
	Blob string
}

// Symlink reports whether path is stored as a symlink according to this listing.
func (m FileModes) Symlink(path string) bool {
	entry, ok := m[path]
	return ok && NormalizeFileMode(entry.Mode) == SymlinkFileMode
}

// Blob returns the object name of path's reviewed-side blob, or "" when unknown.
func (m FileModes) Blob(path string) string {
	return m[path].Blob
}

// RawEntryModes parses the meta token of a "git diff --raw" entry:
//
//	:<srcmode> <dstmode> <srcsha> <dstsha> <status>
//	::<mode1> <mode2> <dstmode> <sha1> <sha2> <dstsha> <status>
//
// The second form is a combined (merge) entry: one leading colon and one source
// column per parent. Returned modes are normalized, so an absent side ("000000")
// comes back as "". ok is false for anything that is not a raw entry.
func RawEntryModes(meta string) (parents []string, dst, status string, ok bool) {
	rest := strings.TrimLeft(meta, ":")
	count := len(meta) - len(rest)
	fields := strings.Fields(rest)
	// One source mode per parent, one destination mode, the same number of object
	// names, and one status column; the paths are separate tokens.
	if count < 1 || len(fields) != 2*(count+1)+1 {
		return nil, "", "", false
	}
	parents = make([]string, 0, count)
	for _, mode := range fields[:count] {
		parents = append(parents, NormalizeFileMode(mode))
	}
	return parents, NormalizeFileMode(fields[count]), fields[len(fields)-1], true
}

// SymlinkModeFromRawEntry picks the mode that describes the side under review of a
// raw entry: the destination when the entry has one, else the parent side. A
// combined deletion has no destination and one source column per parent, and any
// parent that held a symlink means the patch shows a link target — so a symlink
// parent wins over a regular one. Returns "" when no side states a mode.
func SymlinkModeFromRawEntry(parents []string, dst string) string {
	if dst != "" {
		return dst
	}
	fallback := ""
	for _, mode := range parents {
		if mode == SymlinkFileMode {
			return mode
		}
		if fallback == "" {
			fallback = mode
		}
	}
	return fallback
}

// rawEntryBlob extracts the destination object name of a raw entry, given how many
// parents it lists. Returns "" when the entry has no destination blob.
func rawEntryBlob(meta string, parents int) string {
	fields := strings.Fields(strings.TrimLeft(meta, ":"))
	// modes: [0, parents]; object names: [parents+1, 2*parents+1]; then status.
	dst := 2*parents + 1
	if parents < 1 || dst >= len(fields) {
		return ""
	}
	blob := fields[dst]
	if strings.Trim(blob, "0") == "" {
		return ""
	}
	return blob
}

// ParseRawFileModes reads the file mode of every entry in "git diff --raw -z"
// output. An entry is "<meta>\0<path>[\0<newpath>]"; a rename or copy adds the
// second path — the destination, which is the one keyed here.
func ParseRawFileModes(out string) FileModes {
	tokens := strings.Split(out, "\x00")
	modes := FileModes{}
	for i := 0; i < len(tokens); i++ {
		if !strings.HasPrefix(tokens[i], ":") {
			continue
		}
		parents, dst, status, ok := RawEntryModes(tokens[i])
		if !ok {
			continue
		}
		paths := 1
		if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
			paths = 2
		}
		if i+paths >= len(tokens) {
			break
		}
		mode := SymlinkModeFromRawEntry(parents, dst)
		if path := tokens[i+paths]; path != "" && mode != "" {
			// The blob names follow the modes, so the destination's is the one
			// right after the last parent's. It is all zeroes for a deletion,
			// which NormalizeFileMode maps to "" just like an absent mode.
			modes[path] = RawFileEntry{Mode: mode, Blob: rawEntryBlob(tokens[i], len(parents))}
		}
		i += paths
	}
	return modes
}
