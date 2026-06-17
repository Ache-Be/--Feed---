package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"

	"videofeed/redis_client"
)

type publishRequest struct {
	UserID  string `json:"user_id"`
	VideoID string `json:"video_id"`
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
	if req.UserID == "" || req.VideoID == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "missing user_id or video_id"})
		return
	}

	c := redis_client.Get()
	if c == nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "redis client not initialized"})
		return
	}

	key := "feed:timeline:" + req.UserID
	score := time.Now().UnixMilli()

	// Redis ZADD：向 ZSet 中写入一个元素（member=视频ID），并为其指定排序用的 score（发布时间戳）。
	// - key: feed:timeline:{user_id}
	// - member: video_id
	// - score: 发布时刻（毫秒）
	//
	// 这样后续就可以用 ZREVRANGE 按 score 倒序取出“最新发布”的视频ID列表。
	if err := c.ZAdd(r.Context(), key, redis.Z{
		Score:  float64(score),
		Member: req.VideoID,
	}).Err(); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "redis zadd failed"})
		return
	}

	writeJSON(w, http.StatusOK, apiResponse{Code: 0, Msg: "success"})
}

