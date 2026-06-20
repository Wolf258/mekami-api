# mekami-api

Public interface contract for [Mekami](https://github.com/Wolf258/Mekami)
language frontends.

This module contains only the `api.Frontend` interface and the
shared data shapes (`ParseResult`, `Symbol`, `Ref`, `Workspace`,
`ModuleInfo`, `ModuleEntry`). It is the single source of truth
for the contract every language indexer must implement.

`mekami-core` consumes this interface to drive ingestion;
language cores (e.g. `mekami-core-go`) implement it.

## Install

`mekami-api` is consumed transitively. You do not install it
directly — `mekami-core` and any language core pull it in via
`go get`.

```
go get github.com/Wolf258/mekami-api@v0.1.0
```

## Implementing a frontend

A minimal frontend is a Go type that satisfies `api.Frontend`
and registers itself at `init()` time:

```go
package myfrontend

import "github.com/Wolf258/mekami-api/api/v1"

type Frontend struct{}

func (Frontend) Name() string                                { return "mylang" }
func (Frontend) Extensions() []string                        { return []string{".ml"} }
func (Frontend) StructuralFiles() []string                   { return []string{"manifest.toml"} }
func (Frontend) IsIndexable(relPath string) bool             { return true }
func (Frontend) ResolveLayout(root string) (*api.Workspace, error) { return &api.Workspace{}, nil }
func (Frontend) RootModule(root string) (string, error)      { return "", nil }
func (Frontend) ResolveModules(root string) ([]api.ModuleInfo, error) {
    return []api.ModuleInfo{{Dir: root, ModuleID: "mylang"}}, nil
}
func (Frontend) ResolveFile(root, absPath string) (api.FileMeta, error) {
    return api.FileMeta{ModuleID: "mylang", PackageID: "mylang"}, nil
}
func (Frontend) ParseFile(root, relPath, absPath string, hash string, mtime, size int64) (api.ParseResult, error) {
    return api.ParseResult{
        RelPath:   relPath,
        Lang:      "mylang",
        ModuleID:  "mylang",
        PackageID: "mylang",
        Hash:      hash,
        Mtime:     mtime,
        Size:      size,
    }, nil
}

func init() { api.Register(Frontend{}) }
```

The CLI binary blank-imports your package from the generated
`all_gen.go` so your `init()` runs at startup.

## License

MIT. See [LICENSE](./LICENSE).
