package sql

import (
	"crypto/sha256"
	"encoding/hex"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/schema"
)

// schemaSourceFingerprint hashes the source that decides the database schema
// in this package. A declaration is hashed when it is one of:
//
//   - a struct type whose name ends in "Model" (the GORM models, tags
//     included);
//   - a TableName(), Setup() or Ensure*() method (the index repairs main()
//     runs beside the Setup() calls);
//   - (a) any function or method REACHABLE from those through calls inside
//     this package — `r.helper()` on the caller's own receiver resolves to
//     the method of the receiver's type, a bare `helper(...)` to the
//     package-level function — so DDL moved into a lowercase helper
//     (autoMigrate, normalizeEmails, backfillScheduleCron, …) is still seen;
//   - (b) any function or method whose body calls AutoMigrate or Migrator,
//     or calls Exec or Raw while carrying a string literal with CREATE,
//     ALTER, DROP, UPDATE or INSERT — whoever calls it, including a caller
//     outside the package (main()'s backfills);
//   - a package-level const or var that a hashed body names, so a statement
//     kept in a constant moves the hash with the constant.
//
// (a) and (b) are a union on purpose: (a) misses a helper reached through an
// interface or a function value, (b) misses one that migrates through a call
// it does not name. (b) also catches runtime statements (an UPDATE in a
// repository method): editing one asks for a fingerprint-only update, the
// cheap direction.
//
// The probe this was built against: adding
// `CREATE INDEX IF NOT EXISTS … ON workflow_templates (name)` through
// r.db.Exec inside WorkflowTemplateRepository.autoMigrate() left the previous
// fingerprint — Setup/TableName/Ensure*/models only — unchanged and the test
// green; this one moves and the test goes red. TestSchemaFingerprintSeesHelpers
// keeps that probe as a test.
//
// It hashes TOKENS, not text: comments, blank lines and gofmt alignment do not
// move it, so documenting a model never asks for a schema bump. Anything else
// does, including a renamed Go field — the false positive is cheap (update the
// fingerprint alone and say so in the commit), the false negative it replaces
// is an older release migrating a newer schema.
//
// It is a tripwire, not a proof: a model declared outside this package is
// not seen at all.
func schemaSourceFingerprint(t *testing.T) string {
	t.Helper()
	parts := schemaSourceParts(t, ".")
	if len(parts) == 0 {
		t.Fatal("no model or Setup() found: the fingerprint would hash nothing and never change")
	}
	return fingerprintOf(parts)
}

func fingerprintOf(parts []string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:16])
}

// schemaDDLKeyword matches a statement that changes the schema or migrates
// data. Word boundaries keep `updated_at` out.
var schemaDDLKeyword = regexp.MustCompile(`(?i)\b(CREATE|ALTER|DROP|UPDATE|INSERT)\b`)

type fingerprintFunc struct {
	decl   *ast.FuncDecl
	recv   string // receiver type name, "" for a package-level function
	render string
}

// schemaSourceParts returns the sorted, tokenized declarations the
// fingerprint covers for the non-test Go files in dir.
func schemaSourceParts(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)

	var models []string
	funcs := map[string]*fingerprintFunc{} // key: receiver type + "." + name
	values := map[string]string{}          // package-level const/var name -> tokens
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		tokens := func(n ast.Node) string {
			return tokenize(src[fset.Position(n.Pos()).Offset:fset.Position(n.End()).Offset])
		}
		for _, d := range f.Decls {
			switch decl := d.(type) {
			case *ast.GenDecl:
				for _, spec := range decl.Specs {
					switch sp := spec.(type) {
					case *ast.TypeSpec:
						if !strings.HasSuffix(sp.Name.Name, "Model") {
							continue
						}
						if _, isStruct := sp.Type.(*ast.StructType); !isStruct {
							continue
						}
						models = append(models, "type "+sp.Name.Name+" "+tokens(sp.Type))
					case *ast.ValueSpec:
						for _, n := range sp.Names {
							if n.Name == "_" { // the interface assertions; `_` in a body is not a reference
								continue
							}
							values[n.Name] = decl.Tok.String() + " " + tokens(sp)
						}
					}
				}
			case *ast.FuncDecl:
				if decl.Body == nil {
					continue
				}
				fn := &fingerprintFunc{decl: decl, recv: receiverTypeName(decl)}
				if decl.Recv != nil {
					fn.render = "func " + tokens(decl.Recv) + " " + decl.Name.Name + " " + tokens(decl.Body)
				} else {
					fn.render = "func " + decl.Name.Name + " " + tokens(decl.Type) + " " + tokens(decl.Body)
				}
				funcs[fn.recv+"."+decl.Name.Name] = fn
			}
		}
	}

	hashed := map[string]bool{}
	var queue []string
	mark := func(key string) {
		if _, ok := funcs[key]; ok && !hashed[key] {
			hashed[key] = true
			queue = append(queue, key)
		}
	}
	keys := make([]string, 0, len(funcs))
	for k := range funcs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fn := funcs[k]
		name := fn.decl.Name.Name
		root := fn.recv != "" && (name == "Setup" || name == "TableName" || strings.HasPrefix(name, "Ensure"))
		if root || migratesSchema(fn.decl.Body) { // the roots, and (b)
			mark(k)
		}
	}
	// (a): everything reachable from what is already hashed.
	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		fn := funcs[key]
		recvVar := receiverVarName(fn.decl)
		ast.Inspect(fn.decl.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch f := call.Fun.(type) {
			case *ast.Ident:
				mark("." + f.Name)
			case *ast.SelectorExpr:
				if x, ok := f.X.(*ast.Ident); ok && recvVar != "" && x.Name == recvVar {
					mark(fn.recv + "." + f.Sel.Name)
				}
			}
			return true
		})
	}

	parts := append([]string(nil), models...)
	usedValues := map[string]bool{}
	for key := range hashed {
		fn := funcs[key]
		parts = append(parts, fn.render)
		ast.Inspect(fn.decl.Body, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				if _, isValue := values[id.Name]; isValue {
					usedValues[id.Name] = true
				}
			}
			return true
		})
	}
	for name := range usedValues {
		parts = append(parts, values[name])
	}
	sort.Strings(parts)
	// A spec declaring several names was added once per name.
	return compactStrings(parts)
}

// migratesSchema is rule (b).
func migratesSchema(body *ast.BlockStmt) bool {
	callsMigrator, callsExecOrRaw, carriesStatement := false, false, false
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			name := ""
			switch f := x.Fun.(type) {
			case *ast.Ident:
				name = f.Name
			case *ast.SelectorExpr:
				name = f.Sel.Name
			}
			switch name {
			case "AutoMigrate", "Migrator":
				callsMigrator = true
			case "Exec", "Raw":
				callsExecOrRaw = true
			}
		case *ast.BasicLit:
			if x.Kind == token.STRING && schemaDDLKeyword.MatchString(x.Value) {
				carriesStatement = true
			}
		}
		return true
	})
	return callsMigrator || (callsExecOrRaw && carriesStatement)
}

func receiverTypeName(decl *ast.FuncDecl) string {
	if decl.Recv == nil || len(decl.Recv.List) == 0 {
		return ""
	}
	typ := decl.Recv.List[0].Type
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
	}
	if id, ok := typ.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

func receiverVarName(decl *ast.FuncDecl) string {
	if decl.Recv == nil || len(decl.Recv.List) == 0 || len(decl.Recv.List[0].Names) == 0 {
		return ""
	}
	return decl.Recv.List[0].Names[0].Name
}

func compactStrings(sorted []string) []string {
	out := sorted[:0]
	for i, s := range sorted {
		if i == 0 || s != sorted[i-1] {
			out = append(out, s)
		}
	}
	return out
}

// TestSchemaVersionIsBumpedWithTheModels makes forgetting the schema-version
// bump a red test instead of a ledger that lies. When it fails: if the change
// adds or alters a table, a column, an index or a Setup() migration, increase
// schema.Version by one AND set schema.SourceFingerprint to the value below,
// in the same commit. If it changed nothing in the database, set the
// fingerprint alone and say why in the commit message.
func TestSchemaVersionIsBumpedWithTheModels(t *testing.T) {
	got := schemaSourceFingerprint(t)
	if got != schema.SourceFingerprint {
		t.Fatalf("the schema-defining source changed (fingerprint %s, recorded %s at schema.Version %d).\n"+
			"Bump schema.Version in backend/pkg/webapp/domain/schema/version.go and set SourceFingerprint = %q there.\n"+
			"Without the bump an OLDER release is not refused on the schema this change migrates.",
			got, schema.SourceFingerprint, schema.Version, got)
	}
}

// The probe, kept: DDL in a helper Setup() reaches only through a
// lowercase method (rule a), DDL in a function nothing in the package calls
// (rule b), and a statement kept in a package constant must each move the
// fingerprint. Run against the Setup/TableName/Ensure*-only fingerprint, the
// first and third cases did not move it.
func TestSchemaFingerprintSeesHelpers(t *testing.T) {
	const base = `package sql

type WorkflowTemplateModel struct{ ID int }

type WorkflowTemplateRepository struct{ db *DB }

func (r *WorkflowTemplateRepository) Setup() error {
	if err := r.autoMigrate(); err != nil {
		return err
	}
	return r.helperIndex()
}

func (r *WorkflowTemplateRepository) autoMigrate() error {
	return r.db.AutoMigrate(&WorkflowTemplateModel{})
}

func (r *WorkflowTemplateRepository) helperIndex() error { return r.db.Exec(indexSQL).Error }

func backfillSomething(db *DB) error { return db.Exec("UPDATE t SET a = 1").Error }

const indexSQL = "CREATE INDEX IF NOT EXISTS i ON t (a)"
`
	fingerprintOfSource := func(t *testing.T, src string) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "repo.go"), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		return fingerprintOf(schemaSourceParts(t, dir))
	}
	before := fingerprintOfSource(t, base)

	probes := map[string]string{
		"index added in autoMigrate() (reachable, rule a)": strings.Replace(base,
			"return r.db.AutoMigrate(&WorkflowTemplateModel{})",
			"r.db.Exec(\"CREATE INDEX IF NOT EXISTS probe ON workflow_templates (name)\")\n\treturn r.db.AutoMigrate(&WorkflowTemplateModel{})", 1),
		"backfill statement edited in an uncalled function (rule b)": strings.Replace(base,
			"UPDATE t SET a = 1", "UPDATE t SET a = 2", 1),
		"index statement edited in a constant": strings.Replace(base,
			"ON t (a)\"", "ON t (a, b)\"", 1),
	}
	for name, src := range probes {
		if src == base {
			t.Fatalf("%s: the probe edit did not apply", name)
		}
		if fingerprintOfSource(t, src) == before {
			t.Errorf("%s: the fingerprint did not move", name)
		}
	}

	// A comment in a helper is still not a schema change.
	commented := strings.Replace(base, "func (r *WorkflowTemplateRepository) autoMigrate() error {",
		"func (r *WorkflowTemplateRepository) autoMigrate() error { // why", 1)
	if fingerprintOfSource(t, commented) != before {
		t.Error("a comment inside a hashed helper moved the fingerprint")
	}
}

// The fingerprint must ignore comments and layout, or documenting a model
// would demand a schema bump and the bump would stop meaning anything.
func TestSchemaFingerprintIgnoresCommentsAndLayout(t *testing.T) {
	a := tokenize([]byte("struct {\n\tA int `gorm:\"column:a\"`\n}"))
	b := tokenize([]byte("struct { // doc\n\n\tA   int `gorm:\"column:a\"` // why\n}"))
	c := tokenize([]byte("struct {\n\tA int `gorm:\"column:a;not null\"`\n}"))
	if a != b {
		t.Fatalf("comments or layout moved the token string:\n%s\n%s", a, b)
	}
	if a == c {
		t.Fatal("a changed GORM tag did not move the token string")
	}
}

// tokenize renders Go source as its token stream: comments are skipped by
// the scanner and inserted semicolons (line breaks) are dropped, so only code
// remains.
func tokenize(src []byte) string {
	var s scanner.Scanner
	s.Init(token.NewFileSet().AddFile("", -1, len(src)), src, nil, 0)
	var b strings.Builder
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		if tok == token.SEMICOLON && lit == "\n" {
			continue // an inserted semicolon: a line break, not code
		}
		b.WriteString(tok.String())
		if lit != "" && lit != tok.String() {
			b.WriteString(lit)
		}
		b.WriteByte(' ')
	}
	return b.String()
}
