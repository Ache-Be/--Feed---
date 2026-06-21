package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"videofeed/handlers"
	"videofeed/mq_client"
	"videofeed/mysql_client"
	"videofeed/redis_client"
)

func main() {
	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mode := envOrDefault("APP_MODE", "api")
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	fs.StringVar(&mode, "mode", mode, "")
	_ = fs.Parse(os.Args[1:])

	ctx, cancel := context.WithTimeout(rootCtx, 5*time.Second)
	defer cancel()
	if _, err := mysql_client.Init(ctx); err != nil {
		log.Fatalf("init mysql failed: %v", err)
	}
	defer mysql_client.Close()

	if _, err := redis_client.Init(ctx); err != nil {
		log.Printf("redis disabled, fallback to mysql query path: %v", err)
	}
	defer redis_client.Close()
	if _, err := mq_client.Init(ctx); err != nil {
		log.Printf("rabbitmq disabled, fallback to sync fanout: %v", err)
	}
	defer mq_client.Close()

	if strings.EqualFold(mode, "worker") {
		runWorker(rootCtx)
		return
	}

	runAPI(rootCtx)
}

func runAPI(ctx context.Context) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", handlers.UI)
	mux.HandleFunc("/profile/", handlers.UI)
	mux.Handle("/uploads/", http.StripPrefix("/uploads/", http.FileServer(http.Dir(handlers.UploadRootDir()))))
	mux.HandleFunc("/account/register", handlers.Register)
	mux.HandleFunc("/account/login", handlers.Login)
	mux.HandleFunc("/account/logout", handlers.Logout)
	mux.HandleFunc("/account/me", handlers.Me)
	mux.HandleFunc("/account/my_videos", handlers.MyVideos)
	mux.HandleFunc("/account/following_profiles", handlers.FollowingProfiles)
	mux.HandleFunc("/profile/data", handlers.PublicProfile)
	mux.HandleFunc("/account/update_profile", handlers.UpdateProfile)
	mux.HandleFunc("/account/change_password", handlers.ChangePassword)
	mux.HandleFunc("/account/security_question", handlers.SecurityQuestion)
	mux.HandleFunc("/account/reset_password", handlers.ResetPassword)
	mux.HandleFunc("/upload/media", handlers.UploadMedia)
	mux.HandleFunc("/publish", handlers.Publish)
	mux.HandleFunc("/video/detail", handlers.VideoDetail)
	mux.HandleFunc("/video/like", handlers.LikeVideo)
	mux.HandleFunc("/video/unlike", handlers.UnlikeVideo)
	mux.HandleFunc("/video/update", handlers.UpdateVideo)
	mux.HandleFunc("/video/delete", handlers.DeleteVideo)
	mux.HandleFunc("/recommend", handlers.Recommend)
	mux.HandleFunc("/hot", handlers.Hot)
	mux.HandleFunc("/feed", handlers.Feed)
	mux.HandleFunc("/follow", handlers.Follow)
	mux.HandleFunc("/unfollow", handlers.Unfollow)
	mux.HandleFunc("/following", handlers.Following)
	mux.HandleFunc("/home_feed", handlers.HomeFeed)

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

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		log.Printf("api shutting down")

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("api shutdown failed: %v", err)
		}
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("server stopped: %v", err)
		}
	}
}

func runWorker(ctx context.Context) {
	if !mq_client.Enabled() {
		log.Printf("worker idle: rabbitmq not initialized, api will use sync fanout")
		<-ctx.Done()
		log.Printf("worker stopped")
		return
	}
	if redis_client.Get() == nil {
		log.Printf("worker idle: redis not initialized, api will use mysql query path")
		<-ctx.Done()
		log.Printf("worker stopped")
		return
	}

	consumer := fmt.Sprintf("worker-%d", os.Getpid())
	log.Printf("worker started, consumer=%s", consumer)

	for {
		select {
		case <-ctx.Done():
			log.Printf("worker shutting down")
			return
		default:
		}

		err := mq_client.ConsumeFeedEvents(ctx, func(event mq_client.FeedEvent) error {
			return handlers.FanoutToFollowers(ctx, event.AuthorID, event.VideoID, event.Score)
		})
		if err != nil {
			if ctx.Err() != nil {
				log.Printf("worker stopped")
				return
			}
			log.Printf("consume mq failed: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}

		select {
		case <-ctx.Done():
			log.Printf("worker stopped")
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func envOrDefault(key, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	return v
}
