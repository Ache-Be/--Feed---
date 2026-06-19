package mysql_client

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

var db *sql.DB

func Init(ctx context.Context) (*sql.DB, error) {
	addr := envOrDefault("MYSQL_ADDR", "127.0.0.1:3306")
	user := envOrDefault("MYSQL_USER", "root")
	password := os.Getenv("MYSQL_PASSWORD")
	dbName := envOrDefault("MYSQL_DB", "videofeed")

	rootDSN := fmt.Sprintf("%s:%s@tcp(%s)/?charset=utf8mb4&parseTime=true&loc=Local", user, password, addr)
	rootDB, err := sql.Open("mysql", rootDSN)
	if err != nil {
		return nil, err
	}
	defer rootDB.Close()

	rootDB.SetConnMaxLifetime(5 * time.Minute)
	rootDB.SetMaxIdleConns(2)
	rootDB.SetMaxOpenConns(5)

	if err := rootDB.PingContext(ctx); err != nil {
		return nil, err
	}

	if _, err := rootDB.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS "+dbName+" CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); err != nil {
		return nil, err
	}

	dsn := fmt.Sprintf("%s:%s@tcp(%s)/%s?charset=utf8mb4&parseTime=true&loc=Local", user, password, addr, dbName)
	c, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}

	c.SetConnMaxLifetime(5 * time.Minute)
	c.SetMaxIdleConns(10)
	c.SetMaxOpenConns(20)

	if err := c.PingContext(ctx); err != nil {
		_ = c.Close()
		return nil, err
	}

	if err := ensureSchema(ctx, c); err != nil {
		_ = c.Close()
		return nil, err
	}

	db = c
	return db, nil
}

func Get() *sql.DB {
	return db
}

func Close() {
	if db == nil {
		return
	}
	_ = db.Close()
	db = nil
}

func ensureSchema(ctx context.Context, c *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS videos (
			id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
			author_id VARCHAR(64) NOT NULL,
			video_id VARCHAR(128) NOT NULL,
			publish_time BIGINT NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE KEY uk_author_video (author_id, video_id),
			KEY idx_author_publish (author_id, publish_time DESC)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS follows (
			id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
			user_id VARCHAR(64) NOT NULL,
			target_user_id VARCHAR(64) NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE KEY uk_follow (user_id, target_user_id),
			KEY idx_target_user (target_user_id),
			KEY idx_user_id (user_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	}

	for _, stmt := range statements {
		if _, err := c.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func envOrDefault(key, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	return v
}
