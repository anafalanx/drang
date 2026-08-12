package eval

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/anafalanx/drang/internal/value"
)

func makeFilesystemEntries(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, name := range names {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func requireFilesystemErr(t *testing.T, got value.Value, contains string) {
	t.Helper()
	if !got.IsErr() {
		t.Fatalf("got %s, want a catchable filesystem Err", got.Display())
	}
	if !strings.Contains(got.ErrMsg(), contains) {
		t.Fatalf("Err %q does not contain %q", got.ErrMsg(), contains)
	}
}

func TestReadDirEnumerationBoundedAtNextEntry(t *testing.T) {
	dir := t.TempDir()
	makeFilesystemEntries(t, dir, "c.txt", "a.txt", "b.txt")
	setResourceCollectionLimit(t, 2)

	got := callBuiltin(t, "read_dir", str(dir))
	requireFilesystemErr(t, got, "2-element collection limit")
}

func TestFlatGlobBoundsScannedEntriesNotOnlyMatches(t *testing.T) {
	dir := t.TempDir()
	makeFilesystemEntries(t, dir, "c.txt", "a.txt", "b.txt")
	setResourceCollectionLimit(t, 2)

	// No name matches. filepath.Glob used to read all three names into memory
	// before returning an empty result, so a result-only cap could not help.
	got := callBuiltin(t, "glob", str(filepath.Join(dir, "*.none")))
	requireFilesystemErr(t, got, "glob scan exceeds the 2-entry")
}

func TestFlatGlobExactBudgetPreservesLexicalOrder(t *testing.T) {
	dir := t.TempDir()
	makeFilesystemEntries(t, dir, "b.txt", "a.txt")
	setResourceCollectionLimit(t, 2)

	got := callBuiltin(t, "glob", str(filepath.Join(dir, "*.txt")))
	if got.IsErr() {
		t.Fatalf("glob at the exact budget failed: %s", got.Display())
	}
	elems := got.Obj().(*value.Array).Elems
	if len(elems) != 2 || elems[0].AsStr() != filepath.Join(dir, "a.txt") || elems[1].AsStr() != filepath.Join(dir, "b.txt") {
		t.Fatalf("glob order = %s, want [a.txt, b.txt]", got.Display())
	}
}

func TestBoundedFlatGlobMatchesFilepathGlobForOrdinaryPatterns(t *testing.T) {
	dir := t.TempDir()
	makeFilesystemEntries(t, dir,
		"alpha.txt",
		"beta.log",
		"sub/able.txt",
		"sub/baker.log",
		"other/axis.txt",
	)
	patterns := []string{
		filepath.Join(dir, "*.txt"),
		filepath.Join(dir, "?eta.*"),
		filepath.Join(dir, "[ab]*"),
		filepath.Join(dir, "*", "*.txt"),
		filepath.Join(dir, "*.none"),
		filepath.Join(dir, "alpha.txt"),
		filepath.Join(dir, "missing.txt"),
		"*.go", // relative-directory cleaning
		strings.Repeat("literal"+string(filepath.Separator), 300) + "missing.txt",
	}
	if volume := filepath.VolumeName(dir); len(volume) == 2 {
		patterns = append(patterns, volume+"*.definitely-not-a-drang-test-file") // drive-relative cleaning
	}
	for _, pattern := range patterns {
		want, wantErr := filepath.Glob(pattern)
		got, gotErr := globMatch(pattern)
		if (gotErr != nil) != (wantErr != nil) {
			t.Errorf("glob %q error = %v, filepath.Glob error = %v", pattern, gotErr, wantErr)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("glob %q = %v, filepath.Glob = %v", pattern, got, want)
		}
	}

	malformed := filepath.Join(dir, "[")
	if _, err := globMatch(malformed); err == nil {
		t.Fatalf("glob %q accepted a malformed pattern", malformed)
	}
}

func TestCleanGlobDirWindowsRoots(t *testing.T) {
	if filepath.Separator != '\\' {
		t.Skip("Windows path grammar")
	}
	for _, input := range []string{`C:\`, `C:`, `\\server\share\`} {
		volumeLen, got := cleanGlobDir(input)
		if input == `C:` {
			if volumeLen != 2 || got != `C:.` {
				t.Fatalf("cleanGlobDir(%q) = (%d, %q), want (2, %q)", input, volumeLen, got, `C:.`)
			}
			continue
		}
		if volumeLen != len(input) || got != input {
			t.Fatalf("cleanGlobDir(%q) = (%d, %q), want (%d, %q)", input, volumeLen, got, len(input), input)
		}
	}
}

func TestWalkEnumerationBoundedBeforeWideDirectoryMaterialization(t *testing.T) {
	dir := t.TempDir()
	makeFilesystemEntries(t, dir, "c.txt", "a.txt", "b.txt")
	setResourceCollectionLimit(t, 2)

	got := callBuiltin(t, "walk", str(dir))
	requireFilesystemErr(t, got, "walk result exceeds the 2-element")
}

func TestDoublestarEnumerationUsesSharedTreeBudget(t *testing.T) {
	dir := t.TempDir()
	makeFilesystemEntries(t, dir, "a/one.txt", "b/two.txt", "c/three.txt")
	setResourceCollectionLimit(t, 2)

	got := callBuiltin(t, "glob", str(filepath.Join(dir, "**", "*.none")))
	requireFilesystemErr(t, got, "glob scan exceeds the 2-entry")
}

func TestDoublestarMissingOrFileRootIsNoMatch(t *testing.T) {
	parent := t.TempDir()
	file := filepath.Join(parent, "plain.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, pattern := range []string{
		filepath.Join(parent, "missing", "**", "*.txt"),
		filepath.Join(file, "**", "*.txt"),
	} {
		got := callBuiltin(t, "glob", str(pattern))
		if got.IsErr() || got.Obj().Len() != 0 {
			t.Fatalf("glob(%q) = %s, want []", pattern, got.Display())
		}
	}
}

func TestCopyRejectsSameFileIdentityBeforeTruncation(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(src, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := callBuiltin(t, "copy", str(src), str(src))
	requireFilesystemErr(t, got, "same filesystem object")
	if data, err := os.ReadFile(src); err != nil || string(data) != "keep me" {
		t.Fatalf("same-file copy changed source: data=%q err=%v", data, err)
	}

	t.Run("hard-link-alias", func(t *testing.T) {
		alias := filepath.Join(dir, "alias.txt")
		if err := os.Link(src, alias); err != nil {
			t.Skipf("hard links unavailable: %v", err)
		}
		got := callBuiltin(t, "copy", str(src), str(alias))
		requireFilesystemErr(t, got, "same filesystem object")
		if data, err := os.ReadFile(src); err != nil || string(data) != "keep me" {
			t.Fatalf("hard-link copy changed source: data=%q err=%v", data, err)
		}
	})
}

func TestCopyRejectsDirectoryDescendantBeforeMutation(t *testing.T) {
	src := t.TempDir()
	makeFilesystemEntries(t, src, "original.txt")
	dst := filepath.Join(src, "new", "nested-copy")

	got := callBuiltin(t, "copy", str(src), str(dst))
	requireFilesystemErr(t, got, "destination is inside the source directory")
	if _, err := os.Lstat(filepath.Join(src, "new")); !os.IsNotExist(err) {
		t.Fatalf("rejected descendant copy created destination state: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(src, "original.txt")); err != nil || string(data) != "original.txt" {
		t.Fatalf("rejected descendant copy changed source: data=%q err=%v", data, err)
	}

	got = callBuiltin(t, "copy", str(src), str(src))
	requireFilesystemErr(t, got, "same filesystem object")
}

func TestCopyRejectsDescendantThroughSymlinkAncestor(t *testing.T) {
	parent := t.TempDir()
	src := filepath.Join(parent, "source")
	alias := filepath.Join(parent, "alias")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	makeFilesystemEntries(t, src, "original.txt")
	if err := os.Symlink(src, alias); err != nil {
		t.Skipf("directory symlinks/junctions unavailable: %v", err)
	}

	dst := filepath.Join(alias, "nested-copy")
	got := callBuiltin(t, "copy", str(src), str(dst))
	requireFilesystemErr(t, got, "destination is inside the source directory")
	if _, err := os.Lstat(filepath.Join(src, "nested-copy")); !os.IsNotExist(err) {
		t.Fatalf("canonical descendant copy created destination state: %v", err)
	}
}

func TestCopyRejectsDescendantThroughJunctionAncestor(t *testing.T) {
	parent := t.TempDir()
	src := filepath.Join(parent, "source")
	alias := filepath.Join(parent, "alias")
	makeFilesystemEntries(t, src, "original.txt")
	makeDirectoryJunction(t, alias, src)

	dst := filepath.Join(alias, "nested-copy")
	got := callBuiltin(t, "copy", str(src), str(dst))
	requireFilesystemErr(t, got, "destination is inside the source directory")
	if _, err := os.Lstat(filepath.Join(src, "nested-copy")); !os.IsNotExist(err) {
		t.Fatalf("junction descendant copy created destination state: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(src, "original.txt")); err != nil || string(data) != "original.txt" {
		t.Fatalf("junction descendant copy changed source: data=%q err=%v", data, err)
	}
}

func TestCopyRejectsSymlinkDestinationWithoutTouchingTarget(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.txt")
	target := filepath.Join(dir, "target.txt")
	link := filepath.Join(dir, "destination-link.txt")
	if err := os.WriteFile(src, []byte("replacement"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("keep target"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("file symlinks unavailable: %v", err)
	}

	got := callBuiltin(t, "copy", str(src), str(link))
	requireFilesystemErr(t, got, "destination is a symlink")
	if data, err := os.ReadFile(target); err != nil || string(data) != "keep target" {
		t.Fatalf("rejected symlink copy changed target: data=%q err=%v", data, err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("rejected symlink copy replaced link: info=%v err=%v", info, err)
	}
}

func TestCopyRejectsNestedDirectoryRedirectsWithoutOutsideMutation(t *testing.T) {
	redirects := []struct {
		name        string
		errContains string
		make        func(*testing.T, string, string)
	}{
		{
			name:        "directory-symlink",
			errContains: "destination is a symlink or junction",
			make: func(t *testing.T, link, target string) {
				t.Helper()
				if err := os.Symlink(target, link); err != nil {
					t.Skipf("directory symlinks unavailable: %v", err)
				}
				t.Cleanup(func() { _ = os.Remove(link) })
			},
		},
		{
			name:        "junction",
			errContains: "destination is a reparse point",
			make:        makeDirectoryJunction,
		},
	}

	for _, redirect := range redirects {
		t.Run(redirect.name, func(t *testing.T) {
			root := t.TempDir()
			src := filepath.Join(root, "source")
			dst := filepath.Join(root, "destination")
			outside := filepath.Join(root, "outside")
			makeFilesystemEntries(t, src, "nested/payload.txt")
			makeFilesystemEntries(t, outside, "payload.txt")
			if err := os.Mkdir(dst, 0o755); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(dst, "nested")
			redirect.make(t, link, outside)

			got := callBuiltin(t, "copy", str(src), str(dst))
			requireFilesystemErr(t, got, redirect.errContains)
			if data, err := os.ReadFile(filepath.Join(outside, "payload.txt")); err != nil || string(data) != "payload.txt" {
				t.Fatalf("nested redirect changed outside file: data=%q err=%v", data, err)
			}
			if _, err := os.Lstat(link); err != nil {
				t.Fatalf("rejected copy removed the redirect: %v", err)
			}
		})
	}
}

func TestCopyRejectsNestedJunctionBackIntoSource(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "source")
	dst := filepath.Join(root, "destination")
	protected := filepath.Join(src, "protected")
	makeFilesystemEntries(t, src, "redirect/payload.txt", "protected/payload.txt")
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	makeDirectoryJunction(t, filepath.Join(dst, "redirect"), protected)

	got := callBuiltin(t, "copy", str(src), str(dst))
	requireFilesystemErr(t, got, "destination is a reparse point")
	if data, err := os.ReadFile(filepath.Join(protected, "payload.txt")); err != nil || string(data) != "protected/payload.txt" {
		t.Fatalf("nested junction changed source: data=%q err=%v", data, err)
	}
}

func TestCopyRejectsNestedFileSymlinkWithoutReplacingIt(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "source")
	dst := filepath.Join(root, "destination")
	outside := filepath.Join(root, "outside.txt")
	makeFilesystemEntries(t, src, "nested/payload.txt")
	if err := os.MkdirAll(filepath.Join(dst, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("keep outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dst, "nested", "payload.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("file symlinks unavailable: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(link) })

	got := callBuiltin(t, "copy", str(src), str(dst))
	requireFilesystemErr(t, got, "destination is a symlink or junction")
	if data, err := os.ReadFile(outside); err != nil || string(data) != "keep outside" {
		t.Fatalf("nested file symlink changed outside target: data=%q err=%v", data, err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("rejected copy replaced nested file symlink: info=%v err=%v", info, err)
	}
}

func TestCopyMergesOrdinaryNestedDirectories(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "source")
	dst := filepath.Join(root, "destination")
	makeFilesystemEntries(t, src, "nested/new.txt", "nested/replace.txt", "deeper/child/value.txt")
	makeFilesystemEntries(t, dst, "nested/keep.txt", "nested/replace.txt")

	got := callBuiltin(t, "copy", str(src), str(dst))
	if got.IsErr() {
		t.Fatalf("ordinary directory merge failed: %s", got.Display())
	}
	for name, want := range map[string]string{
		"nested/new.txt":         "nested/new.txt",
		"nested/replace.txt":     "nested/replace.txt",
		"nested/keep.txt":        "nested/keep.txt",
		"deeper/child/value.txt": "deeper/child/value.txt",
	} {
		data, err := os.ReadFile(filepath.Join(dst, filepath.FromSlash(name)))
		if err != nil || string(data) != want {
			t.Errorf("merged %s = %q, err=%v, want %q", name, data, err, want)
		}
	}
}

func TestCopyFollowsExplicitDirectorySourceJunctionOnlyAtRoot(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	alias := filepath.Join(root, "source-alias")
	dst := filepath.Join(root, "destination")
	makeFilesystemEntries(t, source, "nested/payload.txt")
	makeDirectoryJunction(t, alias, source)

	got := callBuiltin(t, "copy", str(alias), str(dst))
	if got.IsErr() {
		t.Fatalf("copy through explicit directory source junction failed: %s", got.Display())
	}
	data, err := os.ReadFile(filepath.Join(dst, "nested", "payload.txt"))
	if err != nil || string(data) != "nested/payload.txt" {
		t.Fatalf("copy through source junction = %q, err=%v", data, err)
	}
}

func TestCopyFollowsExplicitDirectorySourceSymlinkOnlyAtRoot(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	alias := filepath.Join(root, "source-alias")
	dst := filepath.Join(root, "destination")
	makeFilesystemEntries(t, source, "nested/payload.txt")
	if err := os.Symlink(source, alias); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(alias) })

	got := callBuiltin(t, "copy", str(alias), str(dst))
	if got.IsErr() {
		t.Fatalf("copy through explicit directory source symlink failed: %s", got.Display())
	}
	data, err := os.ReadFile(filepath.Join(dst, "nested", "payload.txt"))
	if err != nil || string(data) != "nested/payload.txt" {
		t.Fatalf("copy through source symlink = %q, err=%v", data, err)
	}
}

func TestCopyDereferencesNestedSourceFileSymlink(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	dst := filepath.Join(root, "destination")
	target := filepath.Join(root, "target.txt")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("referenced bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(source, "linked.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("file symlinks unavailable: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(link) })

	got := callBuiltin(t, "copy", str(source), str(dst))
	if got.IsErr() {
		t.Fatalf("copy with nested source file symlink failed: %s", got.Display())
	}
	data, err := os.ReadFile(filepath.Join(dst, "linked.txt"))
	if err != nil || string(data) != "referenced bytes" {
		t.Fatalf("nested source symlink copy = %q, err=%v", data, err)
	}
	if info, err := os.Lstat(filepath.Join(dst, "linked.txt")); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("nested source symlink was not materialized as an ordinary file: info=%v err=%v", info, err)
	}
}

func TestCopyDoesNotTraverseNestedSourceJunction(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	dst := filepath.Join(root, "destination")
	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	makeFilesystemEntries(t, outside, "payload.txt")
	makeDirectoryJunction(t, filepath.Join(source, "redirect"), outside)

	got := callBuiltin(t, "copy", str(source), str(dst))
	requireFilesystemErr(t, got, "source tree contains a directory symlink or junction")
	if _, err := os.Lstat(filepath.Join(dst, "redirect", "payload.txt")); !os.IsNotExist(err) {
		t.Fatalf("nested source junction materialized outside contents: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(outside, "payload.txt")); err != nil || string(data) != "payload.txt" {
		t.Fatalf("source junction handling changed outside data: data=%q err=%v", data, err)
	}
}

func makeDirectoryJunction(t *testing.T, link, target string) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("directory junctions require Windows")
	}
	output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Skipf("directory junctions unavailable: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	t.Cleanup(func() { _ = os.Remove(link) })
}

func TestCopyFileFailurePreservesExistingDestination(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.txt")
	dst := filepath.Join(dir, "destination.txt")
	if err := os.WriteFile(src, []byte("replacement"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("previous good data"), 0o644); err != nil {
		t.Fatal(err)
	}

	oldCopy := copyFileData
	copyFileData = func(out io.Writer, _ io.Reader) (int64, error) {
		n, _ := out.Write([]byte("partial"))
		return int64(n), errors.New("injected copy failure")
	}
	t.Cleanup(func() { copyFileData = oldCopy })

	got := callBuiltin(t, "copy", str(src), str(dst))
	requireFilesystemErr(t, got, "injected copy failure")
	if data, err := os.ReadFile(dst); err != nil || string(data) != "previous good data" {
		t.Fatalf("failed staged copy changed destination: data=%q err=%v", data, err)
	}
	if temps, err := filepath.Glob(filepath.Join(dir, ".drang-copy-*")); err != nil || len(temps) != 0 {
		t.Fatalf("failed staged copy left temporaries: %v (err=%v)", temps, err)
	}
}

func TestCopyFileAtomicallyReplacesExistingDestination(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.txt")
	dst := filepath.Join(dir, "destination.txt")
	if err := os.WriteFile(src, []byte("replacement"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("previous"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := callBuiltin(t, "copy", str(src), str(dst))
	if got.IsErr() {
		t.Fatalf("copy replacement failed: %s", got.Display())
	}
	if data, err := os.ReadFile(dst); err != nil || string(data) != "replacement" {
		t.Fatalf("copy replacement = %q, err=%v", data, err)
	}
	if temps, err := filepath.Glob(filepath.Join(dir, ".drang-copy-*")); err != nil || len(temps) != 0 {
		t.Fatalf("successful staged copy left temporaries: %v (err=%v)", temps, err)
	}
}

func TestCopyReplacementDoesNotMutateDestinationHardlinkPeer(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.txt")
	peer := filepath.Join(dir, "peer.txt")
	dst := filepath.Join(dir, "destination.txt")
	if err := os.WriteFile(src, []byte("replacement"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(peer, []byte("shared previous data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(peer, dst); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}

	got := callBuiltin(t, "copy", str(src), str(dst))
	if got.IsErr() {
		t.Fatalf("copy replacement failed: %s", got.Display())
	}
	if data, err := os.ReadFile(dst); err != nil || string(data) != "replacement" {
		t.Fatalf("destination = %q, err=%v", data, err)
	}
	if data, err := os.ReadFile(peer); err != nil || string(data) != "shared previous data" {
		t.Fatalf("atomic replacement mutated hard-link peer: data=%q err=%v", data, err)
	}
}

func TestCopyTraversalHasGlobalEntryBudget(t *testing.T) {
	src := t.TempDir()
	makeFilesystemEntries(t, src, "a.txt", "b.txt", "c.txt")
	dst := filepath.Join(t.TempDir(), "copy")
	setResourceCollectionLimit(t, 2)

	got := callBuiltin(t, "copy", str(src), str(dst))
	requireFilesystemErr(t, got, "copy traversal exceeds the 2-entry")
}
