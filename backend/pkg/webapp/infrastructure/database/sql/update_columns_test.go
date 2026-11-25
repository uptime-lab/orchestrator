package sql

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
)

// An Update that writes an explicit column list persists exactly that list:
// a settable column missing from it answers success and stores nothing. Each
// case below holds EVERY column of the model to one of two places — the list
// the writer selects, or a named exception with the reason the writer leaves
// it alone — so a column added to a model fails here until someone decides
// which it is.
func TestExplicitUpdateColumnListsCoverEveryModelColumn(t *testing.T) {
	cases := []struct {
		name    string
		model   interface{}
		written [][]string
		// leftAlone maps a column the writer deliberately does not write to
		// the reason, so the exception is reviewed where it is granted.
		leftAlone map[string]string
	}{
		{
			name:    "OrganizationalUnitRepository.Update",
			model:   &OrganizationalUnitModel{},
			written: [][]string{ouUpdateColumns, ouPasswordColumns},
			leftAlone: map[string]string{
				"id":         "the row's key",
				"account_id": "the tenant boundary, never moved by an edit",
				"created_at": "set once at insert",
			},
		},
		{
			name:    "AccountPolicyRepository.Upsert",
			model:   &AccountPolicyModel{},
			written: [][]string{accountPolicyUpsertColumns, accountPolicyPasswordColumns},
			leftAlone: map[string]string{
				"account_id": "the conflict target",
				"created_at": "set once at insert",
			},
		},
		{
			name:    "AccessEntryRepository.Update",
			model:   &AccessEntryModel{},
			written: [][]string{accessEntryUpdateColumns},
			leftAlone: map[string]string{
				"id":         "the row's key",
				"account_id": "the tenant boundary",
				"type":       "fixed at creation; the service never changes an entry's type",
				"ou_id":      "legacy single-unit column, superseded by access_entry_ous",
				"created_at": "set once at insert",
			},
		},
		{
			name:    "SecretRepository.Update",
			model:   &SecretModel{},
			written: [][]string{secretUpdateColumns},
			leftAlone: map[string]string{
				"id":           "the row's key",
				"account_id":   "the tenant boundary",
				"created_at":   "set once at insert",
				"last_used_at": "stamped by delivery, not by an edit",
			},
		},
		{
			name:    "UtilityAssignmentRepository.Update",
			model:   &UtilityAssignmentModel{},
			written: [][]string{utilityAssignmentUpdateColumns},
			leftAlone: map[string]string{
				"id":         "the row's key",
				"account_id": "the tenant boundary",
				"utility_id": "fixed at creation; a different utility is a different assignment",
				"ou_id":      "legacy single-unit column, superseded by the OU join table",
				"created_at": "set once at insert",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			columns := modelColumns(t, tc.model)
			written := map[string]bool{}
			for _, list := range tc.written {
				for _, c := range list {
					if written[c] {
						t.Errorf("column %q is listed twice", c)
					}
					written[c] = true
				}
			}
			present := map[string]bool{}
			for _, c := range columns {
				present[c] = true
				_, excepted := tc.leftAlone[c]
				switch {
				case written[c] && excepted:
					t.Errorf("column %q is both written and named as left alone", c)
				case !written[c] && !excepted:
					t.Errorf("column %q is neither written by the update nor named as left alone: "+
						"add it to the writer's column list, or to this case's exceptions with the reason", c)
				}
			}
			for c := range written {
				if !present[c] {
					t.Errorf("the writer selects %q, which is not a column of the model", c)
				}
			}
			for c := range tc.leftAlone {
				if !present[c] {
					t.Errorf("exception %q names no column of the model", c)
				}
			}
		})
	}
}

func modelColumns(t *testing.T, model interface{}) []string {
	t.Helper()
	s, err := schema.Parse(model, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatalf("parsing the model schema: %v", err)
	}
	var out []string
	for _, f := range s.Fields {
		if f.DBName != "" {
			out = append(out, f.DBName)
		}
	}
	sort.Strings(out)
	return out
}

// sqlCapture records every statement GORM renders, so a dry run shows the
// SET clause an Update actually issues.
type sqlCapture struct {
	logger.Interface
	mu   sync.Mutex
	stmt []string
}

func (c *sqlCapture) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	c.mu.Lock()
	c.stmt = append(c.stmt, sql)
	c.mu.Unlock()
}

func capturingDryRunDB(t *testing.T) (*gorm.DB, *sqlCapture) {
	t.Helper()
	capture := &sqlCapture{Interface: logger.Default.LogMode(logger.Silent)}
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: deadPool{}}),
		&gorm.Config{DryRun: true, Logger: capture})
	if err != nil {
		t.Fatalf("opening an undialled handle: %v", err)
	}
	return db, capture
}

// A unit's watchdog declaration is written by the unit's update. The request
// answered 200 and echoed the config while the column stayed NULL, so the
// next read reported no declaration at all. Rendered without a database: the
// assertion is on the statement the update issues.
func TestOrganizationalUnitUpdateWritesTheWatchdogConfig(t *testing.T) {
	db, capture := capturingDryRunDB(t)
	repo := NewOrganizationalUnitRepository(db, passthroughEncryptor{})
	ou := &organizational_unit.OrganizationalUnit{
		ID:             uuid.New(),
		AccountID:      uuid.New(),
		Name:           "unit",
		WatchdogConfig: &node.WatchdogConfig{Enabled: true, AgentHealth: true},
	}
	// A dry run affects no row, so the update reports the unit as missing;
	// only the rendered statement matters here.
	_ = repo.Update(context.Background(), ou)

	var update string
	for _, s := range capture.stmt {
		if strings.HasPrefix(s, "UPDATE") {
			update = s
		}
	}
	if update == "" {
		t.Fatalf("no UPDATE rendered; statements: %q", capture.stmt)
	}
	if !strings.Contains(update, `"watchdog_config"=`) {
		t.Errorf("the update does not write watchdog_config: %s", update)
	}
}

// The node's operator-editable writer names its columns by hand, because a
// node row is written continuously by the device stream. Every field the
// update command lets a caller set must be one of them, or that edit answers
// success and is dropped. The command's JSON names are the column names.
func TestNodeOperatorEditableWriterCoversEveryUpdateCommandField(t *testing.T) {
	db, capture := capturingDryRunDB(t)
	repo := NewNodeRepository(db, passthroughEncryptor{})
	_ = repo.UpdateOperatorEditable(context.Background(), &node.Node{ID: uuid.New(), AccountID: uuid.New(), Name: "n"})

	var update string
	for _, s := range capture.stmt {
		if strings.HasPrefix(s, "UPDATE") {
			update = s
		}
	}
	if update == "" {
		t.Fatalf("no UPDATE rendered; statements: %q", capture.stmt)
	}
	cmdType := reflect.TypeOf(node_update.NodeUpdateCommand{})
	for i := 0; i < cmdType.NumField(); i++ {
		tag := strings.Split(cmdType.Field(i).Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		if !strings.Contains(update, `"`+tag+`"=`) {
			t.Errorf("NodeUpdateCommand.%s (%q) is settable but UpdateOperatorEditable does not write it: %s",
				cmdType.Field(i).Name, tag, update)
		}
	}
}

// The account-policy route answers with the record it saved, so the save
// hands back the timestamps it wrote; a first save reported the zero time.
func TestAccountPolicyUpsertReportsTheTimestampsItWrote(t *testing.T) {
	db, _ := capturingDryRunDB(t)
	repo := NewAccountPolicyRepository(db, passthroughEncryptor{})
	policy := &organizational_unit.AccountPolicy{AccountID: uuid.New()}
	before := time.Now()
	if err := repo.Upsert(context.Background(), policy); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if policy.UpdatedAt.Before(before) || policy.CreatedAt.IsZero() {
		t.Errorf("updated_at = %v, created_at = %v; want the times the save wrote", policy.UpdatedAt, policy.CreatedAt)
	}
}
