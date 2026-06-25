// Package cache 提供缓存防护工具，解决缓存穿透、击穿、雪崩问题。
//
// 使用方式：
//
//	// 缓存穿透防护：空结果自动写入 EMPTY_DB 占位符
//	data, err := cache.Fetch(ctx, "key", 5*time.Minute, func() (string, error) {
//	    return db.Query(...)
//	})
//
//	// 缓存击穿防护（热点 key）：分布式锁 + double-check + spin-wait
//	data, err := cache.FetchWithLock(ctx, "hotkey", 5*time.Minute, func() (string, error) {
//	    return db.Query(...)
//	})
package cache

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"

	"videofeed/redis_client"
)

// ErrNotFound 缓存未命中（包括命中了 EMPTY_DB 占位符）。
var ErrNotFound = errors.New("cache: data not found")

const (
	// EmptyPlaceholder 是缓存穿透防护的占位符。
	// 当查询结果为空时，缓存此值，避免每次请求都穿透到 MySQL。
	EmptyPlaceholder = "__CACHE_EMPTY__"

	// EmptyPlaceholderTTL 空值占位符的过期时间，远短于正常数据。
	// 如果数据后来被创建了，最多等这个时间就能查到新数据。
	EmptyPlaceholderTTL = 10 * time.Second

	// LockPrefix 分布式锁的 key 前缀。
	LockPrefix = "cache:lock:"

	// LockTTL 分布式锁持有时间。
	// spin-wait 的等待方最多等 LockTTL + SpinWaitRetries*SpinWaitInterval 就会超时降级。
	LockTTL = 3 * time.Second

	// SpinWaitRetries spin-wait 最大重试次数。
	SpinWaitRetries = 5

	// SpinWaitInterval 每次 spin-wait 间隔。
	SpinWaitInterval = 20 * time.Millisecond

	// DefaultJitter 默认 TTL 抖动比例（±10%），用于防缓存雪崩。
	DefaultJitter = 0.1
)

var sf singleflight.Group

// ---------- 对外 API ----------

// Fetch 是带三级防护的缓存读取函数：
//
//  1. 缓存穿透防护：fetchFn 返回空字符串时，写入 EMPTY_DB 占位符，
//     后续请求不再穿透到 MySQL。
//
//  2. 缓存击穿防护：用 singleflight 合并同一时刻的同 key 并发请求，
//     只让一个请求去查 MySQL，其余等待结果。
//
//  3. 缓存雪崩防护：TTL 加随机抖动（±10%），避免大量 key 同时过期。
//
// 什么时候用？
//   - 适合读多写少、不要求强一致的场景
//   - 不适合频繁变更的数据（EMPYY_DB 的 10s 窗口内，真实数据已创建但缓存仍是空）
func Fetch(ctx context.Context, key string, baseTTL time.Duration, fetchFn func() (string, error)) (string, error) {
	c := redis_client.Get()
	if c == nil {
		// Redis 不可用，降级直查 MySQL
		return fetchFn()
	}

	// 1. 读缓存
	val, err := c.Get(ctx, key).Result()
	if err == nil {
		if val == EmptyPlaceholder {
			return "", ErrNotFound
		}
		return val, nil
	}

	// 2. singleflight 合并同进程并发请求
	//    同一时刻多个 goroutine 请求同一个 key，只会有一个去查 MySQL
	v, err, _ := sf.Do(key, func() (interface{}, error) {
		return fetchAndCache(ctx, c, key, baseTTL, fetchFn)
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

// FetchWithLock 是带分布式锁的缓存读取函数，比 singleflight 防护更强。
//
// 流程：
//  1. 读缓存（第一次检查）
//  2. 缓存未命中 → 尝试获取分布式锁（SET NX）
//  3. 拿到锁 → double-check（第二次检查缓存，防止并发重复加载）
//  4. double-check 仍未命中 → 查 MySQL 并写缓存 → 释放锁
//  5. 没拿到锁 → spin-wait 轮询等待，直到超时降级直查 MySQL
//
// 什么时候用？
//   - 热点 key（如大V时间线、热门视频详情），并发量极高
//   - 需要在多实例之间协调，singleflight 只能管一个进程
//
// 什么时候不用？
//   - 普通 key，singleflight 就够了
//   - 对延迟极其敏感的场景（spin-wait 最多等待 100ms）
func FetchWithLock(ctx context.Context, key string, baseTTL time.Duration, fetchFn func() (string, error)) (string, error) {
	c := redis_client.Get()
	if c == nil {
		return fetchFn()
	}

	// 1. 第一次检查缓存
	val, err := c.Get(ctx, key).Result()
	if err == nil {
		if val == EmptyPlaceholder {
			return "", ErrNotFound
		}
		return val, nil
	}

	// 2. 尝试获取分布式锁
	lockKey := LockPrefix + key
	lockVal := fmt.Sprintf("%d", time.Now().UnixNano())
	locked, err := c.SetNX(ctx, lockKey, lockVal, LockTTL).Result()
	if err != nil {
		// Redis 异常，降级直查 MySQL
		return fetchFn()
	}

	if locked {
		// 3. 拿到锁 → double-check（第二次检查）
		val, err := c.Get(ctx, key).Result()
		if err == nil {
			_ = c.Del(ctx, lockKey).Err()
			if val == EmptyPlaceholder {
				return "", ErrNotFound
			}
			return val, nil
		}

		// 4. 查 MySQL 并写缓存
		data, err := fetchAndCacheUnsafe(ctx, c, key, baseTTL, fetchFn)
		_ = c.Del(ctx, lockKey).Err()
		if err != nil {
			return "", err
		}
		return data, nil
	}

	// 5. 没拿到锁 → spin-wait 轮询等待
	//    最多等待 SpinWaitRetries * SpinWaitInterval = 100ms
	for i := 0; i < SpinWaitRetries; i++ {
		time.Sleep(SpinWaitInterval)
		val, err := c.Get(ctx, key).Result()
		if err == nil {
			if val == EmptyPlaceholder {
				return "", ErrNotFound
			}
			return val, nil
		}
	}

	// 6. spin-wait 超时 → 降级直查 MySQL
	return fetchFn()
}

// ---------- 内部方法 ----------

// fetchAndCache 查 MySQL 并写缓存，内含 double-check。
// 专门给 singleflight 的回调用。
func fetchAndCache(ctx context.Context, c *redis.Client, key string, baseTTL time.Duration, fetchFn func() (string, error)) (string, error) {
	// double-check：singleflight 回调内部再查一次缓存
	// 防止这种情况：goroutine A 刚查完 MySQL 正在写缓存，goroutine B 在 sf.Do 排队
	// 其实 B 不需要再查 MySQL 了
	val, err := c.Get(ctx, key).Result()
	if err == nil {
		if val == EmptyPlaceholder {
			return "", ErrNotFound
		}
		return val, nil
	}

	data, err := fetchFn()
	if err != nil {
		return "", err
	}

	ttl := addJitter(baseTTL, DefaultJitter)
	if data == "" {
		// 缓存穿透防护：空结果写入 EMPTY_DB 占位符
		c.Set(ctx, key, EmptyPlaceholder, EmptyPlaceholderTTL)
		return "", ErrNotFound
	}

	// 雪崩防护：TTL 加随机抖动
	c.Set(ctx, key, data, ttl)
	return data, nil
}

// fetchAndCacheUnsafe 不 double-check，直接查 MySQL 并写缓存。
// 因为调用方（FetchWithLock）已经在拿锁前后做了两次检查。
func fetchAndCacheUnsafe(ctx context.Context, c *redis.Client, key string, baseTTL time.Duration, fetchFn func() (string, error)) (string, error) {
	data, err := fetchFn()
	if err != nil {
		return "", err
	}

	ttl := addJitter(baseTTL, DefaultJitter)
	if data == "" {
		c.Set(ctx, key, EmptyPlaceholder, EmptyPlaceholderTTL)
		return "", ErrNotFound
	}

	c.Set(ctx, key, data, ttl)
	return data, nil
}

// addJitter 给 TTL 加随机抖动，防缓存雪崩。
//
// 场景：1000 个 key 同时过期，瞬间 1000 个请求打到 MySQL。
// 如果每个 key 的 TTL 是 baseTTL ±10%，过期时间分散开，MySQL 压力就平摊了。
//
// pct=0.1 表示 ±10% 的随机抖动。
func addJitter(d time.Duration, pct float64) time.Duration {
	if pct <= 0 {
		return d
	}
	delta := time.Duration(float64(d) * pct)
	// [0, 2*delta] 的随机偏移，再减去 delta，得到 [-delta, +delta]
	offset := time.Duration(rand.Int63n(int64(delta)*2+1)) - delta
	return d + offset
}
