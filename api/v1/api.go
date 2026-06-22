// Package api/v1 is the public contract every language indexer
// implements. The mekami-core/ingest package consumes a Frontend
// to drive a build; mekami-core itself bundles concrete indexers
// (today: Go) under frontend/. External language packages are
// expected to import this package to register their own frontend.
//
// The package only depends on the standard library. The Workspace
// type re-declared here is intentionally a thin shape that the
// indexer fills in and the core translates to its internal
// modlayout.Workspace; the public surface stays small.
package api

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Workspace describes a multi-module layout (e.g. Go workspace,
// Cargo workspace, Bazel repo). Indexers without a workspace
// concept return IsWorkspace=false and an empty WorkspaceMods.
//
// The fields mirror what the core needs to stamp the right
// meta keys in the SQLite store. Indexers should fill them in
// using their own conventions; the core does the translation.
type Workspace struct {
	IsWorkspace      bool
	WorkFile         string   // absolute path of the workspace manifest (e.g. go.work)
	WorkspaceDir     string   // dir containing the manifest
	WorkspaceMods    []string // absolute dirs of every "use"d / "member" module
	PrimaryModPath   string   // canonical module path of the primary module
	PrimaryModuleDir string   // absolute dir of the primary module (for FS lookups)
}

// FileMeta is the resolved package/module information for a single
// source file. ModuleID and PackageID are language-agnostic
// identifiers (Go: module path + import path; Python: project name
// + dotted path; Rust: crate name + crate::module).
type FileMeta struct {
	ModuleID  string
	PackageID string
	DirRel    string
}

// ModuleInfo describes a single module the frontend discovered
// in the build root. It is the result of Frontend.ResolveModules
// and is the language-agnostic shape mekami-core uses to stamp
// the workspace_modules meta key in the SQLite store.
//
// Dir is the absolute filesystem path of the module root (the
// directory that contains the manifest: go.mod for Go,
// Cargo.toml for Rust, pyproject.toml for Python, etc.).
// ModuleID is the canonical module identifier (Go: the module
// path declared in go.mod; Rust: the crate name from Cargo.toml).
type ModuleInfo struct {
	Dir      string
	ModuleID string
}

// ParseResult is the CPU-only output of parsing a single file.
// The ingest pipeline writes it as-is to the SQLite store; nothing
// in the result struct is language-specific.
type ParseResult struct {
	RelPath     string
	Lang        string
	ModuleID    string
	PackageID   string
	PackageName string
	DirRel      string
	Hash        string
	Mtime       int64
	Size        int64
	Symbols     []Symbol
	Refs        []Ref
}

// SymbolKind is a stable lower-case identifier for the role a symbol
// plays in its package. Constants are exported so external indexers
// can reference them by name.
type SymbolKind string

const (
	KindFunc      SymbolKind = "func"
	KindMethod    SymbolKind = "method"
	KindType      SymbolKind = "type"
	KindVar       SymbolKind = "var"
	KindConst     SymbolKind = "const"
	KindImports   SymbolKind = "imports"  // synthetic anchor symbol for the import block
	KindFuncLit   SymbolKind = "funclit"  // synthetic owner for a top-level *ast.FuncLit (e.g. cobra RunE: closures)
)

// Symbol is a single declaration in a file. The file_id and
// package_id fields are stamped by the core after the indexer
// returns; the indexer must leave them zero.
type Symbol struct {
	ID            int64
	FileID        int64
	PackageID     int64
	Kind          SymbolKind
	Name          string
	QualifiedName string
	StartLine     int
	EndLine       int
	Exported      bool
	Signature     string
	ParentSymbol  *int64
}

// RefKind is a stable lower-case identifier for the kind of
// reference edge between two symbols.
type RefKind string

const (
	RefCall    RefKind = "call"
	RefTypeUse RefKind = "type-use"
	RefImport  RefKind = "import"
	RefValue   RefKind = "value"
)

// Ref is a single reference edge emitted by an indexer.
// FromSymbol is the 0-based index of the originating symbol
// within the same ParseResult.Symbols slice; the core translates
// it to a real DB id during the write phase.
type Ref struct {
	ID          int64
	FromSymbol  int64
	ToQualified string
	Kind        RefKind
	Line        int
}

// ModuleEntry is the persisted form of a single workspace
// module. The core stores one JSON-encoded ModuleEntry per line
// in the workspace_modules meta key; queries.ModuleList parses
// it back. Dir is the absolute filesystem path; Path is the
// canonical module identifier resolved by the frontend. Path
// may be empty for legacy entries that only stored the dir.
type ModuleEntry struct {
	Dir  string `json:"dir"`
	Path string `json:"path"`
}

// Frontend is the contract every language indexer implements.
type Frontend interface {
	// Name returns the lowercase language identifier used on the CLI
	// (--lang) and stored in files.lang (e.g. "go").
	Name() string

	// Extensions lists the file suffixes this frontend claims (with
	// the leading dot, e.g. ".go"). The build walker only visits
	// files whose extension is in this set.
	Extensions() []string

	// ResolveLayout is the per-build preamble: find the workspace
	// and stamp the meta keys the language needs. Implementations
	// are free to skip the workspace concept entirely (returning
	// IsWorkspace=false) if the language has none.
	ResolveLayout(root string) (*Workspace, error)

	// ResolveModules enumerates every module the build root
	// contains. For a workspace, this is the union of `use`d
	// modules; for a single-module repo, it is the root module
	// itself. The core uses this to seed the workspace_modules
	// meta key and to resolve legacy entries that store only
	// the module dir. Returning a single-element slice with the
	// root module is the correct answer for non-workspace
	// projects.
	ResolveModules(root string) ([]ModuleInfo, error)

	// RootModule returns the canonical module identifier for the
	// build root (Go: the module path declared in the nearest
	// go.mod; Python: the project name from pyproject.toml; Rust:
	// the crate name from Cargo.toml). Returns an empty string
	// when the language has no concept of a single root module.
	RootModule(root string) (string, error)

	// ResolveFile maps an absolute file path to its module/package
	// identifiers. Called once per file from the parse worker; the
	// implementation should be safe for concurrent use.
	ResolveFile(root, absPath string) (FileMeta, error)

	// ParseFile reads, parses, and produces symbols/refs for a
	// single file. It performs no I/O against the database and is
	// safe to call concurrently.
	ParseFile(root, relPath, absPath string, hash string, mtime, size int64) (ParseResult, error)

	// StructuralFiles lists the basenames whose edit invalidates
	// the entire index (e.g. "go.mod" for Go, "Cargo.toml" for
	// Rust). Returning nil means "no structural files for this
	// language".
	StructuralFiles() []string

	// IsIndexable reports whether a relative path inside the build
	// root should be ingested. The default skip rules (.git,
	// .mekami, vendor, etc.) are still applied by the walker; this
	// hook is for language-specific exclusions such as Go's
	// _test.go.
	IsIndexable(relPath string) bool
}

// Registry holds the frontends known to the running binary. It is
// populated by package init functions and read-only after main
// starts.
type Registry struct {
	m map[string]Frontend
}

// NewRegistry returns an empty Registry. Tests use this to scope
// the indexer set per scenario; the production binary uses the
// global default registry.
func NewRegistry() *Registry { return &Registry{m: map[string]Frontend{}} }

// Global is the default registry used by ingest.Get. Indexers
// call Register to populate it at init time; the binary's main
// blank-imports the frontend packages that need to register.
var Global = NewRegistry()

// Register adds a frontend to a registry. Duplicate names panic
// so a typo in one frontend is caught at startup.
func (r *Registry) Register(f Frontend) {
	if f == nil {
		panic("api.Register: nil frontend")
	}
	name := f.Name()
	if name == "" {
		panic("api.Register: empty Name()")
	}
	if _, exists := r.m[name]; exists {
		panic(fmt.Sprintf("api.Register: duplicate frontend %q", name))
	}
	r.m[name] = f
}

// Register is a shorthand for Global.Register. External indexers
// can call this from their init() function.
func Register(f Frontend) { Global.Register(f) }

// Get returns the frontend registered under the given name.
func (r *Registry) Get(name string) (Frontend, error) {
	f, ok := r.m[name]
	if !ok {
		names := sortedNames(r.Names())
		if len(names) == 0 {
			return nil, fmt.Errorf("unknown language %q: no cores installed", name)
		}
		return nil, fmt.Errorf("unknown language %q; available: %s", name, names)
	}
	return f, nil
}

// Get is a shorthand for Global.Get.
func Get(name string) (Frontend, error) { return Global.Get(name) }

// Names returns the registered frontend names in sorted order.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.m))
	for n := range r.m {
		out = append(out, n)
	}
	return out
}

// Names is a shorthand for Global.Names.
func Names() []string { return Global.Names() }

// All returns a snapshot of the registered frontends.
func (r *Registry) All() []Frontend {
	out := make([]Frontend, 0, len(r.m))
	for _, f := range r.m {
		out = append(out, f)
	}
	return out
}

// All is a shorthand for Global.All.
func All() []Frontend { return Global.All() }

// IsStructural reports whether rel matches a StructuralFiles()
// entry of any registered frontend. It is used by the watcher
// to decide between an incremental and a full rebuild. The
// empty-rel case is treated as non-structural.
func IsStructural(rel string) bool {
	if rel == "" {
		return false
	}
	base := filepath.Base(rel)
	for _, f := range Global.All() {
		for _, s := range f.StructuralFiles() {
			if s == base {
				return true
			}
		}
	}
	return false
}

// IsStructural reports whether rel matches a StructuralFiles()
// entry of any registered frontend in r.
func (r *Registry) IsStructural(rel string) bool {
	if rel == "" {
		return false
	}
	base := filepath.Base(rel)
	for _, f := range r.All() {
		for _, s := range f.StructuralFiles() {
			if s == base {
				return true
			}
		}
	}
	return false
}

// DefaultStructuralFiles returns the union of StructuralFiles()
// for every registered frontend, sorted and deduplicated. Used
// by the watcher when the language is not known up front.
func DefaultStructuralFiles() []string {
	seen := map[string]struct{}{}
	var out []string
	for _, f := range Global.All() {
		for _, s := range f.StructuralFiles() {
			if _, ok := seen[s]; ok {
				continue
			}
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// DefaultStructuralFiles returns the union for r.
func (r *Registry) DefaultStructuralFiles() []string {
	seen := map[string]struct{}{}
	var out []string
	for _, f := range r.All() {
		for _, s := range f.StructuralFiles() {
			if _, ok := seen[s]; ok {
				continue
			}
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// sortedNames returns the registered names in a quoted, sorted
// form, used in error messages. Internal helper.
func sortedNames(rs []string) string {
	quoted := make([]string, len(rs))
	for i, n := range rs {
		quoted[i] = fmt.Sprintf("%q", n)
	}
	sort.Strings(quoted)
	return strings.Join(quoted, ", ")
}
