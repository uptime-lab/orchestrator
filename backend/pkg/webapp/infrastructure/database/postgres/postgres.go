package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// SlogAdapter adapts slog.Logger to implement gorm's logger.Interface
type SlogAdapter struct {
	SL             *slog.Logger
	LogLevel       logger.LogLevel
	SlowThreshold  time.Duration
	IgnoreNotFound bool
}

// LogMode implements logger.Interface
func (l *SlogAdapter) LogMode(level logger.LogLevel) logger.Interface {
	newLogger := *l
	newLogger.LogLevel = level
	return &newLogger
}

// Info implements logger.Interface
func (l *SlogAdapter) Info(ctx context.Context, msg string, data ...interface{}) {
	if l.LogLevel >= logger.Info {
		l.SL.InfoContext(ctx, fmt.Sprintf(msg, data...))
	}
}

// Warn implements logger.Interface
func (l *SlogAdapter) Warn(ctx context.Context, msg string, data ...interface{}) {
	if l.LogLevel >= logger.Warn {
		l.SL.WarnContext(ctx, fmt.Sprintf(msg, data...))
	}
}

// Error implements logger.Interface
func (l *SlogAdapter) Error(ctx context.Context, msg string, data ...interface{}) {
	if l.LogLevel >= logger.Error {
		l.SL.ErrorContext(ctx, fmt.Sprintf(msg, data...))
	}
}

// Trace implements logger.Interface
func (l *SlogAdapter) Trace(ctx context.Context, begin time.Time, fc func() (sql string, rowsAffected int64), err error) {
	if l.LogLevel <= logger.Silent {
		return
	}

	elapsed := time.Since(begin)
	sql, rows := fc()

	// Create structured log fields
	attrs := []slog.Attr{
		slog.Duration("elapsed", elapsed),
		slog.String("sql", sql),
		slog.Int64("rows", rows),
	}

	if err != nil && l.LogLevel >= logger.Error {
		if !(l.IgnoreNotFound && errors.Is(err, gorm.ErrRecordNotFound)) {
			attrs = append(attrs, slog.String("error", err.Error()))
			l.SL.LogAttrs(ctx, slog.LevelError, "SQL error", attrs...)
		}
		return
	}

	if l.SlowThreshold > 0 && elapsed > l.SlowThreshold && l.LogLevel >= logger.Warn {
		l.SL.LogAttrs(ctx, slog.LevelWarn, "Slow SQL query", attrs...)
		return
	}

	if l.LogLevel >= logger.Info {
		l.SL.LogAttrs(ctx, slog.LevelInfo, "SQL query", attrs...)
	}
}

// NewDatabaseConnection creates a new GORM database connection using slog
func NewDatabaseConnection(cfg config.DatabaseConfig, l *slog.Logger) (*gorm.DB, error) {
	// Build DSN string for PostgreSQL
	dsn := fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.DBName, cfg.SSLMode,
	)

	// GORM log level. logger.Info logs EVERY statement with its parameter values
	// interpolated — useful in development, wrong in production: it is a large
	// volume of disk, and any query touching a credential column writes that
	// value into the container log, which is a far weaker boundary than the
	// encrypted column it came from. Production logs warnings and slow queries
	// only. Controlled by DB_LOG_QUERIES for the rare debugging session.
	gormLevel := logger.Info
	if cfg.ProductionLogging && strings.ToLower(strings.TrimSpace(os.Getenv("DB_LOG_QUERIES"))) != "true" {
		gormLevel = logger.Warn
	}
	logAdapter := &SlogAdapter{
		SL:             l,
		LogLevel:       gormLevel,
		SlowThreshold:  200 * time.Millisecond,
		IgnoreNotFound: true,
	}

	// Configure GORM
	gormConfig := &gorm.Config{
		Logger: logAdapter,
		NowFunc: func() time.Time {
			return time.Now().UTC()
		},
		PrepareStmt:            true,
		SkipDefaultTransaction: true,
		// Translate driver errors to gorm sentinels (gorm.ErrDuplicatedKey etc.)
		// so repositories can map unique violations to domain errors.
		TranslateError: true,
	}

	// Open connection
	db, err := openDatabase(postgres.Open(dsn), gormConfig, databaseStartupWait, time.Sleep, l)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	// Configure connection pool
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get database connection: %w", err)
	}

	// Set connection pool parameters.
	//
	// MaxOpenConns is deliberately BELOW the server's max_connections
	// (Postgres's default of 100). At exactly 100 the backend alone could
	// consume every slot — leaving none for superuser_reserved_connections,
	// none for a psql session, and none for an operator console outside this
	// tree, which draws from the same budget. So the moment the platform was
	// in trouble, the tools for diagnosing it would stop working.
	// Reachable rather than theoretical: the workflow engine runs 500 workers.
	//
	// 60 leaves ~40 slots for the console, ad-hoc psql and the reserved
	// superuser connections. Raising it means raising max_connections first,
	// and connections are not free memory: size both so that max_connections
	// times the per-connection memory (work_mem plus backend overhead) fits the
	// database's memory limit.
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetMaxOpenConns(60)
	sqlDB.SetConnMaxLifetime(time.Hour)

	// Verify connection
	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return db, nil
}

// NewDefaultDatabaseConnection creates a new GORM database connection with default logger
func NewDefaultDatabaseConnection(cfg config.DatabaseConfig) (*gorm.DB, error) {
	defaultLogger := slog.Default()
	return NewDatabaseConnection(cfg, defaultLogger)
}

// databaseStartupWait is how long the server waits for a database that is
// still starting. After a host reboot Docker's restart policy brings the
// containers back in no particular order — compose's depends_on applies to
// `compose up` only — so the server can start before Postgres accepts
// connections, and would otherwise exit and be restarted.
const databaseStartupWait = 60 * time.Second

// openWithStartupWait calls open until it succeeds, retrying only a database
// that is not there YET: a refused or unreachable connection, a name that does
// not resolve yet, or Postgres answering that it is starting up. Anything else
// (a wrong password, a missing database) is returned at once, and the last
// error is returned once wait has passed.
func openWithStartupWait(open func() (*gorm.DB, error), wait time.Duration, sleep func(time.Duration), l *slog.Logger) (*gorm.DB, error) {
	delay := 500 * time.Millisecond
	waited := time.Duration(0)
	for {
		db, err := open()
		if err == nil || !databaseNotUpYet(err) || waited >= wait {
			return db, err
		}
		if waited == 0 {
			l.Info("waiting for the database to accept connections", "error", err, "wait", wait)
		}
		sleep(delay)
		waited += delay
		if delay < 5*time.Second {
			delay *= 2
		}
	}
}

// openDatabase opens the connection through the startup wait with the ORM's
// own logger silenced, and hands the configured logger back once it is open.
//
// gorm.Open logs every failed open at ERROR from inside itself, before the
// wait can tell a database that is still starting from one that is wrong, so
// an ordinary reboot wrote an ERROR line that resolved itself a second later.
// Nothing is hidden by silencing it: the wait logs that it is waiting, and a
// failure it gives up on is returned for the caller to report.
func openDatabase(dialector gorm.Dialector, cfg *gorm.Config, wait time.Duration, sleep func(time.Duration), l *slog.Logger) (*gorm.DB, error) {
	configured := cfg.Logger
	quiet := *cfg
	quiet.Logger = logger.Discard
	db, err := openWithStartupWait(func() (*gorm.DB, error) {
		return gorm.Open(dialector, &quiet)
	}, wait, sleep, l)
	if err != nil {
		return nil, err
	}
	db.Logger = configured
	return db, nil
}

// databaseNotUpYet reports whether err means the database is still starting.
func databaseNotUpYet(err error) bool {
	var opErr *net.OpError
	var dnsErr *net.DNSError
	if errors.As(err, &opErr) || errors.As(err, &dnsErr) {
		return true
	}
	// SQLSTATE 57P03, cannot_connect_now: "the database system is starting up".
	var pgErr interface{ SQLState() string }
	return errors.As(err, &pgErr) && pgErr.SQLState() == "57P03"
}
