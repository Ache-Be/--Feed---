package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"videofeed/cache"
	"videofeed/mq_client"
	"videofeed/mysql_client"
	"videofeed/redis_client"
)

type publishRequest struct {
	UserID      string `json:"user_id"`
	VideoID     string `json:"video_id"`
	Title       string `json:"title"`
	CoverURL    string `json:"cover_url"`
	VideoURL    string `json:"video_url"`
	Description string `json:"description"`
}

// Publish 处理 POST /publish
//
// 核心思路：
// - 每个用户都有一个“时间线”ZSet：key = feed:timeline:{user_id}
// - ZSet 的 member 存视频ID，score 存发布时间戳（毫秒）
//
// 为什么使用 ZSet（有序集合）来做 Feed 时间线？
// - ZSet 天然按 score 排序，适合用时间戳作为 score 来表达“先后顺序”
// - 写入（ZADD）和按时间倒序读取（ZREVRANGE）都很直接
// - 对 MVP 来说，不需要额外的表结构或索引即可实现“按时间倒序取最新 N 条”
func Publish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Code: 1, Msg: "method not allowed"})
		return
	}

	var req publishRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "invalid json"})
		return
	}

	currentUser, err := requireCurrentUsername(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, apiResponse{Code: 1, Msg: "login required"})
		return
	}

	req.UserID = currentUser
	req.VideoID = strings.TrimSpace(req.VideoID)
	req.Title = strings.TrimSpace(req.Title)
	req.CoverURL = strings.TrimSpace(req.CoverURL)
	req.VideoURL = strings.TrimSpace(req.VideoURL)
	req.Description = strings.TrimSpace(req.Description)
	db := mysql_client.Get()
	if db == nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "mysql client not initialized"})
		return
	}
	c := redis_client.Get()

	key := "feed:timeline:" + req.UserID
	score := time.Now().UnixMilli()
	if req.VideoID == "" {
		req.VideoID = fmt.Sprintf("vid_%d", score)
	}

	// Redis ZADD：向 ZSet 中写入一个元素（member=视频ID），并为其指定排序用的 score（发布时间戳）。
	// - key: feed:timeline:{user_id}
	// - member: video_id
	// - score: 发布时刻（毫秒）
	//
	// 这样后续就可以用 ZREVRANGE 按 score 倒序取出“最新发布”的视频ID列表。
	if _, err := db.ExecContext(r.Context(),
		`INSERT INTO videos (author_id, video_id, title, cover_url, video_url, description, publish_time)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE
		 	title = VALUES(title),
		 	cover_url = VALUES(cover_url),
		 	video_url = VALUES(video_url),
		 	description = VALUES(description),
		 	publish_time = VALUES(publish_time)`,
		req.UserID, req.VideoID, req.Title, req.CoverURL, req.VideoURL, req.Description, score,
	); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "mysql insert video failed"})
		return
	}

	// 清除之前可能存在的 EMPTY_DB 占位符，下次查询直接走 L3 获取真实数据
	cache.Evict(r.Context(), fmt.Sprintf("cache:video:%s:%s", req.UserID, req.VideoID))

	if c != nil {
		if err := c.ZAdd(r.Context(), key, redis.Z{
			Score:  float64(score),
			Member: req.VideoID,
		}).Err(); err != nil {
			log.Printf("publish timeline cache update failed: %v", err)
			_ = c.Del(r.Context(), key).Err()
		}

		// 新发布的视频初始化热榜分数为 0，后续互动会逐步拉升
		updateVideoHotScore(r.Context(), req.UserID, req.VideoID)
	}

	if c != nil {
		event := mq_client.FeedEvent{
			AuthorID: req.UserID,
			VideoID:  req.VideoID,
			Score:    score,
		}
		if err := mq_client.PublishFeedEvent(r.Context(), event); err != nil {
			// MQ 不可用时，降级为同步扇出：
			// - 直接在 HTTP 请求内遍历粉丝、写入 inbox ZSet
			// - 延迟会变高（请求内等待），但保证粉丝能及时看到新视频
			// - 面试话术：「MQ 挂了就走同步降级，保证可用性，延迟换一致性」
			log.Printf("publish mq failed, fallback to sync fanout: %v", err)
			if err := FanoutToFollowers(r.Context(), req.UserID, req.VideoID, score); err != nil {
				// 视频已入库，扇出失败不阻塞发布成功返回
				// 面试话术：「视频已落库，扇出是异步优化路径，失败时兜底策略是允许粉丝
				//           下一刷时走 MySQL 直查路径，不丢数据，只影响实时性」
				log.Printf("publish sync fanout failed: %v", err)
			}
		}
	}

	profile, _, err := loadAccountProfileWithPassword(r.Context(), db, req.UserID)
	if err != nil {
		profile = accountProfile{Username: req.UserID, Nickname: req.UserID}
	}

	writeJSON(w, http.StatusOK, apiResponse{
		Code: 0,
		Msg:  "success",
		Data: videoDetail{
			AuthorID:       req.UserID,
			AuthorNickname: profile.Nickname,
			AuthorAvatar:   profile.AvatarURL,
			VideoID:        req.VideoID,
			Title:          req.Title,
			CoverURL:       req.CoverURL,
			VideoURL:       req.VideoURL,
			Description:    req.Description,
			PublishTime:    score,
		},
	})
}
