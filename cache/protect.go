// Package cache 提供缓存防护工具，解决缓存穿透、击穿、雪崩问题。
//
// 三级缓存架构：
//
//	L1：go-cache（进程内存），TTL 5s，亚毫秒返回
//	L2：Redis（分布式），TTL 1h，0.5-1ms 网络往返
//	L3：MySQL（持久化），兜底
//
// 查询路径：L1 → L2 → L3，命中后逐级回写。
// 需要 L1 的原因是：即使 Redis 只有 0.5ms 延迟，热点 key 每秒上万的 QPS
// 累加起来也很可观。L1 在进程内无网络开销，能把 Redis QPS 降低 90%+。
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

	gocache "github.com/patrickmn/go-cache"
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

	// L1DefaultTTL L1 本地缓存的默认 TTL。
	// L1 只缓存高频热点数据，不需要长 TTL。
	L1DefaultTTL = 5 * time.Second

	// L1CleanupInterval L1 本地缓存的清理间隔。
	L1CleanupInterval = 10 * time.Second
)

var (
	sf      singleflight.Group
	l1Cache *gocache.Cache
)

func init() {
	// 初始化 L1 本地缓存
	// 默认 TTL 5s，每 10s 清理一次过期 key
	l1Cache = gocache.New(L1DefaultTTL, L1CleanupInterval)
}

// ---------- 对外 API ----------

// Fetch 是带三级缓存（L1→L2→L3）的缓存读取函数：
//
//  1. 查询 L1（go-cache 进程内存，5s TTL）
//  2. L1 未命中 → 查 L2（Redis，1h TTL），命中写回 L1
//  3. L2 未命中 → 查 L3（MySQL fetchFn），异步写 L2 + 同步写 L1
//
// 内置缓存穿透防护（EMPTY_DB）、缓存击穿防护（singleflight）、
// 缓存雪崩防护（TTL 加随机抖动）。
//
// 什么时候用？
//   - 适合读多写少、不要求强一致的场景
//   - 不适合频繁变更的数据（EMPTY_DB 的 10s 窗口内，真实数据已创建但缓存仍是空）
func Fetch(ctx context.Context, key string, baseTTL time.Duration, fetchFn func() (string, error)) (string, error) {
	// 1. 查 L1（进程内存）
	if val, ok := l1Cache.Get(key); ok {
		s := val.(string)
		if s == EmptyPlaceholder {
			return "", ErrNotFound
		}
		return s, nil
	}

	c := redis_client.Get()
	if c == nil {
		// Redis 不可用，降级直查 MySQL
		data, err := fetchFn()
		if err != nil {
			return "", err
		}
		if data == "" {
			return "", ErrNotFound
		}
		// 写入 L1
		l1Cache.Set(key, data, L1DefaultTTL)
		return data, nil
	}

	// 2. 查 L2（Redis）
	val, err := c.Get(ctx, key).Result()
	if err == nil {
		if val == EmptyPlaceholder {
			l1Cache.Set(key, EmptyPlaceholder, L1DefaultTTL)
			return "", ErrNotFound
		}
		// L2 命中 → 写回 L1
		l1Cache.Set(key, val, L1DefaultTTL)
		return val, nil
	}

	// 3. L1 + L2 都未命中 → singleflight 合并并发，查 L3（MySQL）
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
//  1. 读 L1（go-cache 进程内存）
//  2. L1 未命中 → 读 L2（Redis）
//  3. L2 未命中 → 尝试获取分布式锁（SET NX）
//  4. 拿到锁 → double-check（再次检查 L2 + L1）
//  5. double-check 仍未命中 → 查 L3（MySQL）并写 L2 + L1 → 释放锁
//  6. 没拿到锁 → spin-wait 轮询等待，直到超时降级直查 MySQL
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

	// 1. 查 L1（进程内存）
	if val, ok := l1Cache.Get(key); ok {
		s := val.(string)
		if s == EmptyPlaceholder {
			return "", ErrNotFound
		}
		return s, nil
	}

	// 2. 查 L2（Redis）
	val, err := c.Get(ctx, key).Result()
	if err == nil {
		if val == EmptyPlaceholder {
			l1Cache.Set(key, EmptyPlaceholder, L1DefaultTTL)
			return "", ErrNotFound
		}
		l1Cache.Set(key, val, L1DefaultTTL)
		return val, nil
	}

	// 3. 尝试获取分布式锁
	lockKey := LockPrefix + key
	lockVal := fmt.Sprintf("%d", time.Now().UnixNano())
	locked, err := c.SetNX(ctx, lockKey, lockVal, LockTTL).Result()
	if err != nil {
		// Redis 异常，降级直查 MySQL
		return fetchFn()
	}

	if locked {
		// 4. 拿到锁 → double-check（再次检查 L2 + L1）
		val, err := c.Get(ctx, key).Result()
		if err == nil {
			Unlock(c, lockKey, lockVal) // Lua 原子解锁
			if val == EmptyPlaceholder {
				l1Cache.Set(key, EmptyPlaceholder, L1DefaultTTL)
				return "", ErrNotFound
			}
			l1Cache.Set(key, val, L1DefaultTTL)
			return val, nil
		}

		// 5. 查 MySQL 并写 L2 + L1
		data, err := fetchAndCacheUnsafe(ctx, c, key, baseTTL, fetchFn)
		Unlock(c, lockKey, lockVal) // Lua 原子解锁
		if err != nil {
			return "", err
		}
		return data, nil
	}

	// 6. 没拿到锁 → spin-wait 轮询等待
	//    最多等待 SpinWaitRetries * SpinWaitInterval = 100ms
	for i := 0; i < SpinWaitRetries; i++ {
		time.Sleep(SpinWaitInterval)
		val, err := c.Get(ctx, key).Result()
		if err == nil {
			if val == EmptyPlaceholder {
				l1Cache.Set(key, EmptyPlaceholder, L1DefaultTTL)
				return "", ErrNotFound
			}
			l1Cache.Set(key, val, L1DefaultTTL)
			return val, nil
		}
	}

	// 7. spin-wait 超时 → 降级直查 MySQL
	return fetchFn()
}

// ---------- Lua 脚本（原子操作） ----------

// unlockScript 分布式锁解锁脚本。
// Redis 单线程模型下，Lua 脚本内的 GET+比较+DEL 连续执行，不会被其他命令插入。
//
//	KEYS[1] = lockKey
//	ARGV[1] = token（锁持有者的标识）
//
// 如果锁的值 == token → DEL，返回 1（自己的锁）
// 否则 → 返回 0（锁已过期或被别人持有）
var unlockScript = redis.NewScript(`
    if redis.call("GET", KEYS[1]) == ARGV[1] then
        return redis.call("DEL", KEYS[1])
    end
    return 0
`)

// rateLimitScript 限流脚本。
// 在 Redis 单线程模型下，INCR 和 PEXPIRE 合成原子操作。
//
//	KEYS[1] = 限流 key（如 "ratelimit:like:user_123"）
//	ARGV[1] = 窗口 TTL（毫秒，如 1000）
//	ARGV[2] = 窗口内最大次数（如 10）
//
// 返回值：
//
//	0 = 未超限，允许通过
//	1 = 已超限，拒绝
var rateLimitScript = redis.NewScript(`
    local count = redis.call("INCR", KEYS[1])
    if count == 1 then
        redis.call("PEXPIRE", KEYS[1], ARGV[1])
    end
    if count > tonumber(ARGV[2]) then
        return 1
    end
    return 0
`)

// Unlock 原子释放分布式锁（Lua 脚本实现）。
func Unlock(c *redis.Client, lockKey, token string) {
	unlockScript.Run(context.Background(), c, []string{lockKey}, token)
}

// RateLimit 检查操作是否超限（滑动窗口近似）。
//
// key 格式建议：ratelimit:{action}:{user_id}（如 ratelimit:like:user_123）
// windowMs：窗口时长（毫秒），如 1000 = 1 秒
// maxCount：窗口内最大允许次数，如 10 = 每秒最多 10 次
//
// 返回 true 表示未超限（放行），false 表示超限（拒绝）。
func RateLimit(ctx context.Context, c *redis.Client, key string, windowMs int64, maxCount int64) bool {
	if c == nil {
		return true // Redis 挂了直接放行，不做限流
	}
	ret, err := rateLimitScript.Run(ctx, c, []string{key}, windowMs, maxCount).Int()
	if err != nil {
		return true // 脚本执行异常也放行，不阻塞业务
	}
	return ret == 0
}

// ---------- 内部方法 ----------

// fetchAndCache 查 MySQL 并写 L2（Redis）和 L1（go-cache）。
// 专门给 singleflight 的回调用。
func fetchAndCache(ctx context.Context, c *redis.Client, key string, baseTTL time.Duration, fetchFn func() (string, error)) (string, error) {
	// double-check：singleflight 回调内部再查一次 L2
	val, err := c.Get(ctx, key).Result()
	if err == nil {
		if val == EmptyPlaceholder {
			l1Cache.Set(key, EmptyPlaceholder, L1DefaultTTL)
			return "", ErrNotFound
		}
		l1Cache.Set(key, val, L1DefaultTTL)
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
		l1Cache.Set(key, EmptyPlaceholder, L1DefaultTTL)
		return "", ErrNotFound
	}

	// 写 L2（Redis），雪崩防护：TTL 加随机抖动
	c.Set(ctx, key, data, ttl)
	// 写 L1（进程内存）
	l1Cache.Set(key, data, L1DefaultTTL)
	return data, nil
}

// fetchAndCacheUnsafe 不 double-check，直接查 MySQL 并写 L2 + L1。
// 因为调用方（FetchWithLock）已经在拿锁前后做了两次检查。
func fetchAndCacheUnsafe(ctx context.Context, c *redis.Client, key string, baseTTL time.Duration, fetchFn func() (string, error)) (string, error) {
	data, err := fetchFn()
	if err != nil {
		return "", err
	}

	ttl := addJitter(baseTTL, DefaultJitter)
	if data == "" {
		c.Set(ctx, key, EmptyPlaceholder, EmptyPlaceholderTTL)
		l1Cache.Set(key, EmptyPlaceholder, L1DefaultTTL)
		return "", ErrNotFound
	}

	// 写 L2（Redis）
	c.Set(ctx, key, data, ttl)
	// 写 L1（进程内存）
	l1Cache.Set(key, data, L1DefaultTTL)
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

// FlushL1Cache 清除 L1 本地缓存（用于测试或缓存失效场景）。
func FlushL1Cache() {
	l1Cache.Flush()
}

// SetL1 直接写入 L1 缓存（用于数据变更时主动失效）。
func SetL1(key string, val string) {
	l1Cache.Set(key, val, L1DefaultTTL)
}

// DelL1 主动删除 L1 缓存。
func DelL1(key string) {
	l1Cache.Delete(key)
}

// Evict 主动删除指定 key 的 L1 + L2 缓存（清除 EMPTY_DB 占位符）。
//
// 使用场景：数据创建/更新后调用，清除之前可能写入的 EMPTY_DB，
// 下次查询直接走 L3 获取真实数据，无需等待 EmptyPlaceholderTTL 过期。
//
// 例如发布视频后：
//
//	cache.Evict(ctx, fmt.Sprintf("cache:video:%s:%s", authorID, videoID))
func Evict(ctx context.Context, key string) {
	// 删除 L1（进程内存）
	l1Cache.Delete(key)

	// 删除 L2（Redis）
	c := redis_client.Get()
	if c == nil {
		return
	}
	if err := c.Del(ctx, key).Err(); err != nil {
		// 删除失败没关系，最多等 EmptyPlaceholderTTL（10s）自动过期
		_ = err
	}
}
