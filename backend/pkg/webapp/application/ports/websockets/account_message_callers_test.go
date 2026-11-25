package websockets

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// webappRoot is backend/pkg/webapp, the tree every NewAccountMessage sender
// lives in.
var webappRoot = filepath.Join("..", "..", "..")

// Every account-global WebSocket frame is filtered by the action its sender
// declares, and an EMPTY action is fail-closed: the frame reaches nobody
// (mayReceive). The signature already forces a caller to pass something; this
// test forces the something to be a named authz.Action* constant, so that an
// event whose permission nobody decided cannot compile past review as
// `authz.Action("")`, `""` or a variable that happens to be empty at run time.
//
// It is a source scan rather than a type because Go has no non-empty string
// type: the alternative — a registry of allowed event types the hub consults —
// would be a second list beside the callers, and this repo's other cross-tree
// guards (routes_documented_test, frontend_any_scope_test) already show that a
// scan over the real source is the version that does not drift. The check is
// deliberately narrow: an ast walk for calls named NewAccountMessage whose
// second argument is anything but `authz.<Ident>`. A sender that genuinely
// needs a computed action extends this test with its reasoning rather than
// working around it.
func TestNewAccountMessageCallersDeclareANamedAction(t *testing.T) {
	var calls int
	var problems []string

	err := filepath.WalkDir(webappRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", path, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isNewAccountMessage(call.Fun) {
				return true
			}
			calls++
			at := fset.Position(call.Pos())
			if len(call.Args) != 3 {
				problems = append(problems, fmt.Sprintf("%s: NewAccountMessage called with %d arguments", at, len(call.Args)))
				return true
			}
			if reason := actionArgProblem(call.Args[1]); reason != "" {
				problems = append(problems, fmt.Sprintf("%s: %s", at, reason))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// The four senders that existed when this guard was written. Finding none
	// means the scan is looking in the wrong place or the constructor was
	// renamed, and a guard that inspects nothing passes forever.
	if calls == 0 {
		t.Fatalf("no NewAccountMessage call found under %s; the scan or the constructor moved and this test moves with it", webappRoot)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

// isNewAccountMessage matches both spellings of the call: qualified from
// another package (websockets3.NewAccountMessage) and bare from this one.
func isNewAccountMessage(fun ast.Expr) bool {
	switch f := fun.(type) {
	case *ast.SelectorExpr:
		return f.Sel.Name == "NewAccountMessage"
	case *ast.Ident:
		return f.Name == "NewAccountMessage"
	}
	return false
}

// actionArgProblem returns why the action argument is not acceptable, or "".
func actionArgProblem(arg ast.Expr) string {
	switch a := arg.(type) {
	case *ast.SelectorExpr:
		pkg, ok := a.X.(*ast.Ident)
		if ok && pkg.Name == "authz" && strings.HasPrefix(a.Sel.Name, "Action") {
			return ""
		}
		return fmt.Sprintf("action argument %s is not an authz.Action* constant", exprString(a))
	case *ast.BasicLit:
		return fmt.Sprintf("action argument is a literal %s; an account-global event's action is the one its REST list route is gated on, passed as the named authz constant", a.Value)
	case *ast.CallExpr:
		return fmt.Sprintf("action argument %s is a conversion, not a named authz.Action* constant — `authz.Action(\"\")` would compile and deliver the frame to nobody", exprString(a))
	default:
		return fmt.Sprintf("action argument %s is not a named authz.Action* constant; the scan cannot verify a value computed at run time", exprString(arg))
	}
}

func exprString(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return exprString(x.X) + "." + x.Sel.Name
	case *ast.BasicLit:
		return x.Value
	case *ast.CallExpr:
		return exprString(x.Fun) + "(...)"
	default:
		return fmt.Sprintf("%T", e)
	}
}
