package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"videofeed/handlers"
	"videofeed/redis_client"
)

func main() {
	// 初始化 Redis 连接：服务启动时就验证 Redis 是否可用（Ping）。
	// 如果 Redis 连接失败，直接退出，避免服务“假启动”。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := redis_client.Init(ctx); err != nil {
		log.Fatalf("init redis failed: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/publish", handlers.Publish)
	mux.HandleFunc("/feed", handlers.Feed)

	// 额外提供一个简单的健康检查接口（可选但不影响核心要求）
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	addr := ":8080"
	log.Printf("server listening on %s", addr)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server stopped: %v", err)
	}
}
