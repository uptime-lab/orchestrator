package main

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// backendEntitlementSource is the server's authority for the free cap,
// relative to this module. Both directories ship in the public repository,
// so the pin holds there too. The signer must not IMPORT the backend module
// (it is FCL-licensed and a separate module), so the constant is read as
// source.
var backendEntitlementSource = filepath.Join("..", "..", "backend", "pkg", "webapp", "domain", "entitlement", "document.go")

// freeDeviceCapFromSource returns the integer literal FreeDeviceCap is
// declared as in the Go file at path.
func freeDeviceCapFromSource(path string) (int, error) {
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return 0, err
	}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if name.Name != "FreeDeviceCap" || i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.INT {
					return 0, errors.New("FreeDeviceCap is no longer an integer literal; teach this test to read it")
				}
				n, err := strconv.Atoi(lit.Value)
				return n, err
			}
		}
	}
	return 0, errors.New("no FreeDeviceCap constant")
}

// The signer's floor is a COPY of the server's entitlement.FreeDeviceCap. A
// server that raised its free cap while the signer kept the old one would
// let the signer issue files the server clamps — the case the floor exists
// to prevent. Skipped only when the backend tree is absent AND the run is
// not strict (CI and the release preflight set ORCHESTRATOR_CONTRACT_STRICT=1,
// where a missing tree fails).
func TestFreeDeviceCapMatchesTheServer(t *testing.T) {
	if _, err := os.Stat(backendEntitlementSource); errors.Is(err, fs.ErrNotExist) {
		if os.Getenv("ORCHESTRATOR_CONTRACT_STRICT") == "1" {
			t.Fatalf("%s is absent and ORCHESTRATOR_CONTRACT_STRICT=1: the free-cap pin cannot be checked", backendEntitlementSource)
		}
		t.Skipf("%s is absent (signer built outside the repository); set ORCHESTRATOR_CONTRACT_STRICT=1 to make this a failure", backendEntitlementSource)
	}
	server, err := freeDeviceCapFromSource(backendEntitlementSource)
	if err != nil {
		t.Fatalf("reading %s: %v", backendEntitlementSource, err)
	}
	if server != freeDeviceCap {
		t.Fatalf("the signer's freeDeviceCap is %d but the server's entitlement.FreeDeviceCap is %d; change main.go to match",
			freeDeviceCap, server)
	}
}

// The reader itself must find a differing value, or the pin above could
// never fail.
func TestFreeDeviceCapReaderSeesTheLiteral(t *testing.T) {
	path := filepath.Join(t.TempDir(), "document.go")
	src := "package entitlement\n\n// doc\nconst FreeDeviceCap = 40\n"
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := freeDeviceCapFromSource(path)
	if err != nil || got != 40 {
		t.Fatalf("got %d, %v; want 40", got, err)
	}
}
