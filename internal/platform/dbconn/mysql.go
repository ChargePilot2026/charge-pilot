package dbconn

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DSN converts the deployment URL to a database/sql MySQL DSN. The URL is
// validated before any network connection is attempted.
func DSN(raw string, multiStatements bool) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "mysql" || u.Host == "" || u.User == nil || u.User.Username() == "" {
		return "", errors.New("invalid MySQL URL")
	}
	name := strings.TrimPrefix(u.Path, "/")
	if name == "" || strings.Contains(name, "/") {
		return "", errors.New("MySQL URL must name one schema")
	}
	port := u.Port()
	if port == "" {
		port = "3306"
	}
	password, _ := u.User.Password()
	config := mysql.NewConfig()
	config.User = u.User.Username()
	config.Passwd = password
	config.Net = "tcp"
	config.Addr = net.JoinHostPort(u.Hostname(), port)
	config.DBName = name
	config.ParseTime = true
	config.Loc = time.UTC
	config.Timeout = 5 * time.Second
	config.ReadTimeout = 10 * time.Second
	config.WriteTimeout = 10 * time.Second
	config.MultiStatements = multiStatements
	config.Params = map[string]string{"charset": "utf8mb4"}
	return config.FormatDSN(), nil
}

func Open(ctx context.Context, raw string) (*sql.DB, error) {
	dsn, err := DSN(raw, false)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("MySQL ping: %w", err)
	}
	return db, nil
}

// WrapGORM configures the application ORM over an already validated and
// pinged MySQL pool. Goose continues to use Open's database/sql handle.
func WrapGORM(db *sql.DB) (*gorm.DB, error) {
	if db == nil {
		return nil, errors.New("nil MySQL pool")
	}
	return gorm.Open(gormmysql.New(gormmysql.Config{
		Conn:                      db,
		SkipInitializeWithVersion: true,
	}), &gorm.Config{
		DisableAutomaticPing:                     true,
		DisableForeignKeyConstraintWhenMigrating: true,
		Logger:                                   logger.Default.LogMode(logger.Silent),
	})
}
