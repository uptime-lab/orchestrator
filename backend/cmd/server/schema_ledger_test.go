package main

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/schema"
)

type fakeLedger struct {
	ledger   schema.Ledger
	readErr  error
	recorded []schema.Entry
	marked   []string
	calls    []string
}

func (f *fakeLedger) MarkMigrated(_ context.Context, version int, revision string) error {
	f.calls = append(f.calls, "mark")
	f.marked = append(f.marked, fmt.Sprintf("%d/%s", version, revision))
	return nil
}

func (f *fakeLedger) Read(context.Context) (schema.Ledger, error) {
	f.calls = append(f.calls, "read")
	return f.ledger, f.readErr
}

func (f *fakeLedger) Record(_ context.Context, e schema.Entry) (bool, error) {
	f.calls = append(f.calls, "record")
	f.recorded = append(f.recorded, e)
	return true, nil
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestCheckSchemaLedgerRefusesANewerSchema(t *testing.T) {
	repo := &fakeLedger{ledger: schema.Ledger{Present: true, Highest: 4}}
	_, err := checkSchemaLedger(context.Background(), repo, 3, quietLogger())
	var newer *schema.NewerSchemaError
	if !errors.As(err, &newer) {
		t.Fatalf("err = %v, want *schema.NewerSchemaError", err)
	}
	if newer.Stored != 4 || newer.Build != 3 {
		t.Fatalf("refusal names %d/%d, want 4/3", newer.Stored, newer.Build)
	}
}

func TestCheckSchemaLedgerFailsClosedOnAReadError(t *testing.T) {
	repo := &fakeLedger{readErr: errors.New("connection reset")}
	_, err := checkSchemaLedger(context.Background(), repo, 3, quietLogger())
	if err == nil {
		t.Fatal("a ledger read error must refuse the boot")
	}
	if !strings.Contains(err.Error(), "NOT touched") || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("refusal %q must name the cause and that the database was not touched", err)
	}
}

func TestCheckSchemaLedgerProceedsOnAbsentOlderAndEqual(t *testing.T) {
	for _, l := range []schema.Ledger{{}, {Present: true, Highest: 2}, {Present: true, Highest: 3}} {
		repo := &fakeLedger{ledger: l}
		got, err := checkSchemaLedger(context.Background(), repo, 3, quietLogger())
		if err != nil {
			t.Fatalf("ledger %+v refused: %v", l, err)
		}
		if got != l {
			t.Fatalf("returned %+v, want the ledger as read %+v", got, l)
		}
		if len(repo.recorded) != 0 {
			t.Fatalf("the check wrote a row: %+v", repo.recorded)
		}
	}
}

// The intent row is written unfinished (migrated=false), after the ledger
// table's own DDL; the finish is a separate mark.
func TestRecordSchemaIntentCreatesTheTableAndWritesAnUnfinishedRow(t *testing.T) {
	repo := &fakeLedger{}
	setup := func() error { repo.calls = append(repo.calls, "setup"); return nil }
	if err := recordSchemaIntent(context.Background(), repo, setup, schema.Ledger{Present: true, Highest: 1, Complete: true}, 2, "abc", quietLogger()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(repo.calls, ",") != "setup,record" {
		t.Fatalf("calls = %v, want setup then record", repo.calls)
	}
	want := schema.Entry{SchemaVersion: 2, BuildRevision: "abc", PreviousVersion: 1, Migrated: false}
	if len(repo.recorded) != 1 || repo.recorded[0] != want {
		t.Fatalf("recorded %+v, want %+v", repo.recorded, want)
	}
	if err := markSchemaMigrated(context.Background(), repo, 2, "abc", quietLogger()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(repo.marked, ",") != "2/abc" {
		t.Fatalf("marked = %v", repo.marked)
	}
}

func TestRecordSchemaIntentWritesNothingWhenTheTableCannotBeCreated(t *testing.T) {
	repo := &fakeLedger{}
	err := recordSchemaIntent(context.Background(), repo, func() error { return errors.New("ddl refused") }, schema.Ledger{}, 1, "", quietLogger())
	if err == nil || len(repo.recorded) != 0 {
		t.Fatalf("err=%v recorded=%v: a failed Setup must stop the write", err, repo.recorded)
	}
}

// TestSchemaLedgerCheckRunsBeforeAnySetup holds main() to the order the
// refusal depends on. A refusal is worth something only if NO migration ran
// before it: the check must come before the first call that can execute DDL
// (any .Setup(), .AutoMigrate(), .Exec() or Ensure*() call). The intent row
// (recordSchemaIntent, whose only DDL is the ledger table's own) comes after
// the check and before that first product DDL, so a migration that fails
// part-way still leaves a row; the row is marked finished only after the
// last repository Setup().
//
// main() is the composition root and cannot be run in a test without a
// database and the whole service graph, so the order is read from its syntax
// tree: the calls in main's body, in source order, which is execution order
// for this straight-line block.
func TestSchemaLedgerCheckRunsBeforeAnySetup(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var mainFn *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == "main" {
			mainFn = fd
		}
	}
	if mainFn == nil {
		t.Fatal("no func main in main.go")
	}

	var check, intent, record, firstDDL, lastSetup token.Pos
	var firstDDLName string
	ast.Inspect(mainFn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		var name string
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			name = fn.Name
		case *ast.SelectorExpr:
			name = fn.Sel.Name
		default:
			return true
		}
		pos := call.Pos()
		switch {
		case name == "checkSchemaLedger":
			if check == token.NoPos {
				check = pos
			}
		case name == "recordSchemaIntent":
			if intent == token.NoPos {
				intent = pos
			}
		case name == "markSchemaMigrated":
			record = pos
		case name == "Setup" || name == "AutoMigrate" || name == "Exec" || strings.HasPrefix(name, "Ensure"):
			if firstDDL == token.NoPos || pos < firstDDL {
				firstDDL, firstDDLName = pos, name
			}
			if name == "Setup" && pos > lastSetup {
				lastSetup = pos
			}
		}
		return true
	})

	if check == token.NoPos {
		t.Fatal("main() no longer calls checkSchemaLedger: a newer schema would be migrated by an older build")
	}
	if intent == token.NoPos {
		t.Fatal("main() no longer calls recordSchemaIntent: a migration that fails part-way would leave no row, and an older build would start on its tables")
	}
	if record == token.NoPos {
		t.Fatal("main() no longer calls markSchemaMigrated: the ledger would never record a finished upgrade")
	}
	if firstDDL == token.NoPos || lastSetup == token.NoPos {
		t.Fatal("found no Setup() call in main(): this test's view of main.go is stale")
	}
	if check > firstDDL {
		t.Fatalf("checkSchemaLedger (%s) runs AFTER %s() (%s): a refused boot would already have altered the database",
			fset.Position(check), firstDDLName, fset.Position(firstDDL))
	}
	if intent < check || intent > firstDDL {
		t.Fatalf("recordSchemaIntent (%s) must run after checkSchemaLedger (%s) and before the first product DDL %s() (%s)",
			fset.Position(intent), fset.Position(check), firstDDLName, fset.Position(firstDDL))
	}
	if record < lastSetup {
		t.Fatalf("markSchemaMigrated (%s) runs BEFORE the last Setup() (%s): a failed migration would be recorded as finished",
			fset.Position(record), fset.Position(lastSetup))
	}
}
