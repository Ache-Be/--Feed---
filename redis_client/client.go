package redis_client

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
)

var client *redis.Client

// Init 初始化 Redis 客户端，并进行一次 Ping 来验证连接是否正常。
//
// 为什么要在启动时 Ping？
// - 这样可以在服务启动阶段就发现 Redis 配置错误、Redis 未启动等问题，
//   避免服务看似启动成功但所有接口都在运行时失败。
func Init(ctx context.Context) (*redis.Client, error) {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}

	password := os.Getenv("REDIS_PASSWORD")
	db := 0

	c := redis.NewClient(&redis.Options{
		Addr:         addr,
		Password:     password,
		DB:           db,
		DialTimeout:  3 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
	})

	if err := c.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis ping failed, addr=%s: %w", addr, err)
	}

	client = c
	return client, nil
}

// Get 返回全局 Redis 客户端。
// 该函数用于在 handler 层获取 Redis 实例，避免在各处重复初始化连接。
func Get() *redis.Client {
	return client
}

