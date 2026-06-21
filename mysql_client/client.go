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
		`CREATE TABLE IF NOT EXISTS users (
			id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
			username VARCHAR(64) NOT NULL,
			password_hash VARCHAR(128) NOT NULL,
			nickname VARCHAR(64) NOT NULL DEFAULT '',
			avatar_url VARCHAR(512) NOT NULL DEFAULT '',
			age INT NULL,
			address VARCHAR(255) NOT NULL DEFAULT '',
			signature VARCHAR(255) NOT NULL DEFAULT '',
			security_question VARCHAR(255) NOT NULL DEFAULT '',
			security_answer_hash VARCHAR(128) NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE KEY uk_username (username)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS videos (
			id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
			author_id VARCHAR(64) NOT NULL,
			video_id VARCHAR(128) NOT NULL,
			title VARCHAR(255) NOT NULL DEFAULT '',
			cover_url VARCHAR(512) NOT NULL DEFAULT '',
			video_url VARCHAR(512) NOT NULL DEFAULT '',
			description TEXT,
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
		`CREATE TABLE IF NOT EXISTS video_likes (
			id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
			user_id VARCHAR(64) NOT NULL,
			author_id VARCHAR(64) NOT NULL,
			video_id VARCHAR(128) NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE KEY uk_video_like (user_id, author_id, video_id),
			KEY idx_like_author_video (author_id, video_id),
			KEY idx_like_user (user_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	}

	for _, stmt := range statements {
		if _, err := c.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}

	userColumns := []struct {
		name string
		ddl  string
	}{
		{name: "nickname", ddl: "ALTER TABLE users ADD COLUMN nickname VARCHAR(64) NOT NULL DEFAULT '' AFTER password_hash"},
		{name: "avatar_url", ddl: "ALTER TABLE users ADD COLUMN avatar_url VARCHAR(512) NOT NULL DEFAULT '' AFTER nickname"},
		{name: "age", ddl: "ALTER TABLE users ADD COLUMN age INT NULL AFTER nickname"},
		{name: "address", ddl: "ALTER TABLE users ADD COLUMN address VARCHAR(255) NOT NULL DEFAULT '' AFTER age"},
		{name: "signature", ddl: "ALTER TABLE users ADD COLUMN signature VARCHAR(255) NOT NULL DEFAULT '' AFTER address"},
		{name: "security_question", ddl: "ALTER TABLE users ADD COLUMN security_question VARCHAR(255) NOT NULL DEFAULT '' AFTER signature"},
		{name: "security_answer_hash", ddl: "ALTER TABLE users ADD COLUMN security_answer_hash VARCHAR(128) NOT NULL DEFAULT '' AFTER security_question"},
	}
	for _, col := range userColumns {
		if err := ensureColumn(ctx, c, "users", col.name, col.ddl); err != nil {
			return err
		}
	}

	videoColumns := []struct {
		name string
		ddl  string
	}{
		{name: "title", ddl: "ALTER TABLE videos ADD COLUMN title VARCHAR(255) NOT NULL DEFAULT '' AFTER video_id"},
		{name: "cover_url", ddl: "ALTER TABLE videos ADD COLUMN cover_url VARCHAR(512) NOT NULL DEFAULT '' AFTER title"},
		{name: "video_url", ddl: "ALTER TABLE videos ADD COLUMN video_url VARCHAR(512) NOT NULL DEFAULT '' AFTER cover_url"},
		{name: "description", ddl: "ALTER TABLE videos ADD COLUMN description TEXT NULL AFTER video_url"},
	}
	for _, col := range videoColumns {
		if err := ensureColumn(ctx, c, "videos", col.name, col.ddl); err != nil {
			return err
		}
	}

	if _, err := c.ExecContext(ctx,
		`UPDATE users
		 SET nickname = CASE WHEN nickname = '' THEN username ELSE nickname END,
		     security_question = CASE WHEN security_question = '' THEN '默认问题：请输入默认答案 123456' ELSE security_question END,
		     security_answer_hash = CASE WHEN security_answer_hash = '' THEN SHA2('123456', 256) ELSE security_answer_hash END`,
	); err != nil {
		return err
	}
	return nil
}

func ensureColumn(ctx context.Context, c *sql.DB, tableName, columnName, ddl string) error {
	var exists int
	if err := c.QueryRowContext(ctx,
		`SELECT COUNT(1)
		 FROM information_schema.COLUMNS
		 WHERE TABLE_SCHEMA = DATABASE()
		   AND TABLE_NAME = ?
		   AND COLUMN_NAME = ?`,
		tableName, columnName,
	).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return nil
	}
	_, err := c.ExecContext(ctx, ddl)
	return err
}

func envOrDefault(key, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	return v
}
