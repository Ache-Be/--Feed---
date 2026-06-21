package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"

	"videofeed/mysql_client"
	"videofeed/redis_client"
)

type followRequest struct {
	UserID       string `json:"user_id"`
	TargetUserID string `json:"target_user_id"`
}

type timelineItem struct {
	videoID string
	score   int64
}

type videoDetail struct {
	AuthorID       string `json:"author_id"`
	AuthorNickname string `json:"author_nickname,omitempty"`
	AuthorAvatar   string `json:"author_avatar,omitempty"`
	VideoID        string `json:"video_id"`
	Title          string `json:"title"`
	CoverURL       string `json:"cover_url"`
	VideoURL       string `json:"video_url"`
	Description    string `json:"description,omitempty"`
	PublishTime    int64  `json:"publish_time"`
	LikeCount      int64  `json:"like_count"`
	Liked          bool   `json:"liked"`
}

type videoRef struct {
	authorID string
	videoID  string
	score    int64
}

func followingKey(userID string) string { return "relation:following:" + userID }
func followersKey(userID string) string { return "relation:followers:" + userID }
func inboxKey(userID string) string     { return "feed:inbox:" + userID }

func inboxMember(authorID, videoID string) string { return authorID + "|" + videoID }

func Follow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Code: 1, Msg: "method not allowed"})
		return
	}

	var req followRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "invalid json"})
		return
	}
	req.UserID = strings.TrimSpace(req.UserID)
	req.TargetUserID = strings.TrimSpace(req.TargetUserID)
	if req.UserID == "" || req.TargetUserID == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "missing user_id or target_user_id"})
		return
	}
	if req.UserID == req.TargetUserID {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "cannot follow self"})
		return
	}

	db := mysql_client.Get()
	if db == nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "mysql client not initialized"})
		return
	}
	c := redis_client.Get()

	exists, err := isFollowing(r.Context(), req.UserID, req.TargetUserID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "check follow status failed"})
		return
	}
	if exists {
		writeJSON(w, http.StatusOK, apiResponse{Code: 0, Msg: "already followed"})
		return
	}

	if _, err := db.ExecContext(r.Context(),
		`INSERT INTO follows (user_id, target_user_id) VALUES (?, ?)
		 ON DUPLICATE KEY UPDATE target_user_id = VALUES(target_user_id)`,
		req.UserID, req.TargetUserID,
	); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "mysql insert follow failed"})
		return
	}

	if c != nil {
		_, err = c.TxPipelined(r.Context(), func(p redis.Pipeliner) error {
			p.SAdd(r.Context(), followingKey(req.UserID), req.TargetUserID)
			p.SAdd(r.Context(), followersKey(req.TargetUserID), req.UserID)
			return nil
		})
		if err != nil {
			log.Printf("follow cache update failed: %v", err)
			_ = c.Del(r.Context(), followingKey(req.UserID), followersKey(req.TargetUserID)).Err()
		}

		if err := backfillInboxFromOutbox(r.Context(), req.UserID, req.TargetUserID, 50); err != nil {
			log.Printf("follow inbox backfill skipped: %v", err)
			_ = c.Del(r.Context(), inboxKey(req.UserID)).Err()
		}
	}

	writeJSON(w, http.StatusOK, apiResponse{Code: 0, Msg: "success"})
}

func Unfollow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Code: 1, Msg: "method not allowed"})
		return
	}

	var req followRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "invalid json"})
		return
	}
	req.UserID = strings.TrimSpace(req.UserID)
	req.TargetUserID = strings.TrimSpace(req.TargetUserID)
	if req.UserID == "" || req.TargetUserID == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "missing user_id or target_user_id"})
		return
	}
	if req.UserID == req.TargetUserID {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "cannot unfollow self"})
		return
	}

	db := mysql_client.Get()
	if db == nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "mysql client not initialized"})
		return
	}
	c := redis_client.Get()

	exists, err := isFollowing(r.Context(), req.UserID, req.TargetUserID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "check follow status failed"})
		return
	}
	if !exists {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "not following"})
		return
	}

	if _, err := db.ExecContext(r.Context(), "DELETE FROM follows WHERE user_id = ? AND target_user_id = ?", req.UserID, req.TargetUserID); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "mysql delete follow failed"})
		return
	}

	if c != nil {
		_, err = c.TxPipelined(r.Context(), func(p redis.Pipeliner) error {
			p.SRem(r.Context(), followingKey(req.UserID), req.TargetUserID)
			p.SRem(r.Context(), followersKey(req.TargetUserID), req.UserID)
			p.Del(r.Context(), inboxKey(req.UserID))
			return nil
		})
		if err != nil {
			log.Printf("unfollow cache update failed: %v", err)
			_ = c.Del(r.Context(), followingKey(req.UserID), followersKey(req.TargetUserID), inboxKey(req.UserID)).Err()
		}
	}

	writeJSON(w, http.StatusOK, apiResponse{Code: 0, Msg: "success"})
}

func Following(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Code: 1, Msg: "method not allowed"})
		return
	}

	userID := strings.TrimSpace(r.URL.Query().Get("user_id"))
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "missing user_id"})
		return
	}

	users, err := loadFollowing(r.Context(), userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "load following failed"})
		return
	}
	sort.Strings(users)
	writeJSON(w, http.StatusOK, apiResponse{Code: 0, Data: users})
}

// Feed 处理 GET /feed?user_id=123&limit=20&cursor_score=1710000000000&cursor_video_id=vid_001
//
// 读取逻辑：
// - 从用户对应的时间线 ZSet 中，按 score 倒序取最新 N 条视频ID（默认 20）
// - 支持“游标分页”（cursor），用于继续拉取更旧的数据
//
// 什么是游标分页（Cursor Pagination）？
// - 第 1 页：从“现在”往回取最新 N 条
// - 返回 next_cursor（本页最后一条的 (score, video_id) 组合）
// - 第 2 页：带上 cursor_score + cursor_video_id，再取 N 条，就能拿到“更旧”的下一页
//
// 为什么要做游标分页，而不是 page=1&page=2 这种页码分页？
// - 页码分页需要 offset（跳过前面很多条），当数据量大时会变慢
// - 新数据插入会让页码对应的内容发生漂移，容易出现重复/漏数据
// - 游标分页天然是“从某个时间点继续往后翻”，更适合 Feed/Timeline 这种场景
//
// 为什么游标需要“双字段”（cursor_score + cursor_video_id）？
// - 仅用 score 时，为避免重复通常会用“开区间” (< lastScore)
// - 但当同一毫秒内写入多条（score 相同），开区间会把同 score 的剩余数据整体跳过，导致漏数据
// - 使用 (score, member) 可以精确定位翻页位置：下一页只取严格“小于”上一页最后元素的部分，从而不重不漏
func Feed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Code: 1, Msg: "method not allowed"})
		return
	}

	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "missing user_id"})
		return
	}

	limit := int64(20)
	if s := r.URL.Query().Get("limit"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil || v <= 0 {
			writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "invalid limit"})
			return
		}
		if v > 100 {
			v = 100
		}
		limit = v
	}

	var (
		hasCursor     bool
		cursorScore   int64
		cursorVideoID string
	)
	if scoreStr := r.URL.Query().Get("cursor_score"); scoreStr != "" {
		v, err := strconv.ParseInt(scoreStr, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "invalid cursor_score"})
			return
		}
		cursorScore = v
		hasCursor = true
	}
	if hasCursor {
		cursorVideoID = r.URL.Query().Get("cursor_video_id")
		if strings.TrimSpace(cursorVideoID) == "" {
			writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "missing cursor_video_id"})
			return
		}
	}

	collected := make([]timelineItem, 0, limit+1)
	c := redis_client.Get()
	key := "feed:timeline:" + userID
	if c != nil {
		// Redis ZREVRANGEBYSCORE：按 score 从大到小（倒序）取出 member 列表（可带 LIMIT）。
		//
		// 路线 A（严谨版游标）实现思路：
		// - 只用 score 做游标时，为了不重复通常会用“开区间” (< lastScore)
		// - 但同分数（同毫秒）数据可能很多，开区间会造成漏数据
		// - 因此这里采用双字段游标 (score, video_id)，用它模拟“严格小于上一页最后元素”的翻页
		//
		// 具体做法：
		// - 每次用 Max=当前 cursorScore（或 +inf）取一批（chunk）数据（倒序）
		// - 对于 score==cursorScore 的元素，跳过 member >= cursorVideoID 的部分（包括 cursor 自身）
		// - 收集到 limit+1 条就停止（多取 1 条用于判断是否还有下一页）
		// - 如果不够，再把 cursor 更新为“本次批量结果的最后一个元素”，继续下一轮查询
		currentHasCursor := hasCursor
		currentScore := cursorScore
		currentVideoID := cursorVideoID

		chunkCount := int64(200)
		if chunkCount < limit*3 {
			chunkCount = limit * 3
		}
		if chunkCount > 1000 {
			chunkCount = 1000
		}

		for len(collected) < int(limit)+1 {
			max := "+inf"
			if currentHasCursor {
				max = strconv.FormatInt(currentScore, 10)
			}

			res, err := c.ZRevRangeByScoreWithScores(r.Context(), key, &redis.ZRangeBy{
				Max:    max,
				Min:    "-inf",
				Offset: 0,
				Count:  chunkCount,
			}).Result()
			if err != nil {
				log.Printf("feed redis query failed, fallback to mysql: %v", err)
				collected = collected[:0]
				break
			}
			if len(res) == 0 {
				break
			}

			for _, z := range res {
				score := int64(z.Score)
				member := fmt.Sprint(z.Member)

				if currentHasCursor {
					if score > currentScore {
						continue
					}
					if score == currentScore && member >= currentVideoID {
						continue
					}
				}

				collected = append(collected, timelineItem{videoID: member, score: score})
				if len(collected) >= int(limit)+1 {
					break
				}
			}

			last := res[len(res)-1]
			currentHasCursor = true
			currentScore = int64(last.Score)
			currentVideoID = fmt.Sprint(last.Member)

			if len(res) < int(chunkCount) {
				break
			}
		}
	}

	if len(collected) == 0 && mysql_client.Get() != nil {
		items, err := queryAuthorVideos(r.Context(), userID, hasCursor, cursorScore, cursorVideoID, limit+1)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "mysql query videos failed"})
			return
		}
		collected = items
		if c != nil && len(items) > 0 {
			_, _ = c.TxPipelined(r.Context(), func(p redis.Pipeliner) error {
				for _, it := range items {
					p.ZAdd(r.Context(), key, redis.Z{Score: float64(it.score), Member: it.videoID})
				}
				return nil
			})
		}
	}

	hasMore := len(collected) > int(limit)
	if hasMore {
		collected = collected[:limit]
	}

	videoRefs := make([]videoRef, 0, len(collected))
	for _, it := range collected {
		videoRefs = append(videoRefs, videoRef{
			authorID: userID,
			videoID:  it.videoID,
			score:    it.score,
		})
	}

	videos, err := loadVideoDetails(r.Context(), videoRefs)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "mysql load video details failed"})
		return
	}
	currentUser, _ := currentUsername(r)
	if err := attachLikeStatsToVideos(r.Context(), currentUser, videos); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "load video likes failed"})
		return
	}

	var nextCursor *cursorToken
	if hasMore && len(collected) > 0 {
		last := collected[len(collected)-1]
		nextCursor = &cursorToken{Score: last.score, VideoID: last.videoID}
	}

	writeJSON(w, http.StatusOK, apiResponse{Code: 0, Data: videos, NextCursor: nextCursor})
}

func HomeFeed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Code: 1, Msg: "method not allowed"})
		return
	}

	userID := strings.TrimSpace(r.URL.Query().Get("user_id"))
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "missing user_id"})
		return
	}

	limit := int64(20)
	if s := r.URL.Query().Get("limit"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil || v <= 0 {
			writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "invalid limit"})
			return
		}
		if v > 100 {
			v = 100
		}
		limit = v
	}

	var (
		hasCursor     bool
		cursorScore   int64
		cursorVideoID string
	)
	if scoreStr := r.URL.Query().Get("cursor_score"); scoreStr != "" {
		v, err := strconv.ParseInt(scoreStr, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "invalid cursor_score"})
			return
		}
		cursorScore = v
		hasCursor = true
	}
	if hasCursor {
		cursorVideoID = strings.TrimSpace(r.URL.Query().Get("cursor_video_id"))
		if cursorVideoID == "" {
			writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "missing cursor_video_id"})
			return
		}
	}

	type item struct {
		member string
		score  int64
	}
	collected := make([]item, 0, limit+1)
	c := redis_client.Get()
	key := inboxKey(userID)
	if c != nil {
		currentHasCursor := hasCursor
		currentScore := cursorScore
		currentMember := cursorVideoID

		chunkCount := int64(200)
		if chunkCount < limit*3 {
			chunkCount = limit * 3
		}
		if chunkCount > 1000 {
			chunkCount = 1000
		}

		for len(collected) < int(limit)+1 {
			max := "+inf"
			if currentHasCursor {
				max = strconv.FormatInt(currentScore, 10)
			}
			res, err := c.ZRevRangeByScoreWithScores(r.Context(), key, &redis.ZRangeBy{
				Max:    max,
				Min:    "-inf",
				Offset: 0,
				Count:  chunkCount,
			}).Result()
			if err != nil {
				log.Printf("home feed redis query failed, fallback to mysql: %v", err)
				collected = collected[:0]
				break
			}
			if len(res) == 0 {
				break
			}

			for _, z := range res {
				score := int64(z.Score)
				member := fmt.Sprint(z.Member)

				if currentHasCursor {
					if score > currentScore {
						continue
					}
					if score == currentScore && member >= currentMember {
						continue
					}
				}

				collected = append(collected, item{member: member, score: score})
				if len(collected) >= int(limit)+1 {
					break
				}
			}

			last := res[len(res)-1]
			currentHasCursor = true
			currentScore = int64(last.Score)
			currentMember = fmt.Sprint(last.Member)

			if len(res) < int(chunkCount) {
				break
			}
		}
	}

	if len(collected) == 0 && mysql_client.Get() != nil {
		rows, err := queryHomeFeed(r.Context(), userID, hasCursor, cursorScore, cursorVideoID, limit+1)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "mysql query home feed failed"})
			return
		}
		for _, row := range rows {
			collected = append(collected, item{
				member: inboxMember(row.AuthorID, row.VideoID),
				score:  row.PublishTime,
			})
		}
		if c != nil && len(rows) > 0 {
			_, _ = c.TxPipelined(r.Context(), func(p redis.Pipeliner) error {
				for _, row := range rows {
					p.ZAdd(r.Context(), key, redis.Z{
						Score:  float64(row.PublishTime),
						Member: inboxMember(row.AuthorID, row.VideoID),
					})
				}
				return nil
			})
		}
	}

	hasMore := len(collected) > int(limit)
	if hasMore {
		collected = collected[:limit]
	}

	videoRefs := make([]videoRef, 0, len(collected))
	for _, it := range collected {
		parts := strings.SplitN(it.member, "|", 2)
		authorID := ""
		videoID := it.member
		if len(parts) == 2 {
			authorID = parts[0]
			videoID = parts[1]
		}
		videoRefs = append(videoRefs, videoRef{
			authorID: authorID,
			videoID:  videoID,
			score:    it.score,
		})
	}

	videos, err := loadVideoDetails(r.Context(), videoRefs)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "mysql load video details failed"})
		return
	}
	currentUser, _ := currentUsername(r)
	if err := attachLikeStatsToVideos(r.Context(), currentUser, videos); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "load video likes failed"})
		return
	}

	var nextCursor *cursorToken
	if hasMore && len(collected) > 0 {
		last := collected[len(collected)-1]
		nextCursor = &cursorToken{Score: last.score, VideoID: last.member}
	}

	writeJSON(w, http.StatusOK, apiResponse{Code: 0, Data: videos, NextCursor: nextCursor})
}

func FanoutToFollowers(ctx context.Context, authorID, videoID string, score int64) error {
	c := redis_client.Get()
	if c == nil {
		return nil
	}

	followers, err := loadFollowers(ctx, authorID)
	if err != nil {
		return err
	}
	if len(followers) == 0 {
		return nil
	}

	member := inboxMember(authorID, videoID)
	_, err = c.TxPipelined(ctx, func(p redis.Pipeliner) error {
		for _, fid := range followers {
			fid = strings.TrimSpace(fid)
			if fid == "" {
				continue
			}
			p.ZAdd(ctx, inboxKey(fid), redis.Z{Score: float64(score), Member: member})
		}
		return nil
	})
	return err
}

func isFollowing(ctx context.Context, userID, targetUserID string) (bool, error) {
	c := redis_client.Get()
	if c != nil {
		exists, err := c.SIsMember(ctx, followingKey(userID), targetUserID).Result()
		if err == nil && exists {
			return true, nil
		}
		if err != nil {
			log.Printf("follow cache read failed, fallback to mysql: %v", err)
		}
	}

	db := mysql_client.Get()
	if db == nil {
		return false, nil
	}

	var found int
	if err := db.QueryRowContext(ctx, "SELECT 1 FROM follows WHERE user_id = ? AND target_user_id = ? LIMIT 1", userID, targetUserID).Scan(&found); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func loadFollowing(ctx context.Context, userID string) ([]string, error) {
	c := redis_client.Get()
	var users []string
	if c != nil {
		var err error
		users, err = c.SMembers(ctx, followingKey(userID)).Result()
		if err == nil && len(users) > 0 {
			return users, nil
		}
		if err != nil {
			log.Printf("following cache read failed, fallback to mysql: %v", err)
		}
	}

	db := mysql_client.Get()
	if db == nil {
		return nil, nil
	}

	rows, err := db.QueryContext(ctx, "SELECT target_user_id FROM follows WHERE user_id = ? ORDER BY target_user_id ASC", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	users = users[:0]
	for rows.Next() {
		var target string
		if err := rows.Scan(&target); err != nil {
			return nil, err
		}
		users = append(users, target)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if c != nil && len(users) > 0 {
		_ = c.Del(ctx, followingKey(userID)).Err()
		values := make([]interface{}, 0, len(users))
		for _, v := range users {
			values = append(values, v)
		}
		_ = c.SAdd(ctx, followingKey(userID), values...).Err()
	}
	return users, nil
}

func loadFollowers(ctx context.Context, authorID string) ([]string, error) {
	c := redis_client.Get()
	var users []string
	if c != nil {
		var err error
		users, err = c.SMembers(ctx, followersKey(authorID)).Result()
		if err == nil && len(users) > 0 {
			return users, nil
		}
		if err != nil {
			log.Printf("followers cache read failed, fallback to mysql: %v", err)
		}
	}

	db := mysql_client.Get()
	if db == nil {
		return nil, nil
	}

	rows, err := db.QueryContext(ctx, "SELECT user_id FROM follows WHERE target_user_id = ? ORDER BY user_id ASC", authorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	users = users[:0]
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			return nil, err
		}
		users = append(users, userID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if c != nil && len(users) > 0 {
		_ = c.Del(ctx, followersKey(authorID)).Err()
		values := make([]interface{}, 0, len(users))
		for _, v := range users {
			values = append(values, v)
		}
		_ = c.SAdd(ctx, followersKey(authorID), values...).Err()
	}
	return users, nil
}

func backfillInboxFromOutbox(ctx context.Context, userID, targetUserID string, limit int64) error {
	c := redis_client.Get()
	if c == nil {
		return nil
	}

	zs, err := c.ZRevRangeWithScores(ctx, "feed:timeline:"+targetUserID, 0, limit-1).Result()
	if err != nil {
		return err
	}
	if len(zs) == 0 && mysql_client.Get() != nil {
		items, err := queryAuthorVideos(ctx, targetUserID, false, 0, "", limit)
		if err != nil {
			return err
		}
		zs = make([]redis.Z, 0, len(items))
		for _, it := range items {
			zs = append(zs, redis.Z{
				Score:  float64(it.score),
				Member: it.videoID,
			})
		}
		if len(zs) > 0 {
			_, _ = c.TxPipelined(ctx, func(p redis.Pipeliner) error {
				for _, z := range zs {
					p.ZAdd(ctx, "feed:timeline:"+targetUserID, z)
				}
				return nil
			})
		}
	}
	if len(zs) == 0 {
		return nil
	}

	_, err = c.TxPipelined(ctx, func(p redis.Pipeliner) error {
		for _, z := range zs {
			videoID := fmt.Sprint(z.Member)
			p.ZAdd(ctx, inboxKey(userID), redis.Z{
				Score:  z.Score,
				Member: inboxMember(targetUserID, videoID),
			})
		}
		return nil
	})
	return err
}

func queryAuthorVideos(ctx context.Context, authorID string, hasCursor bool, cursorScore int64, cursorVideoID string, limit int64) ([]timelineItem, error) {
	db := mysql_client.Get()
	if db == nil {
		return nil, nil
	}

	var (
		rows *sql.Rows
		err  error
	)
	if hasCursor {
		rows, err = db.QueryContext(ctx,
			`SELECT video_id, publish_time
			 FROM videos
			 WHERE author_id = ?
			   AND (publish_time < ? OR (publish_time = ? AND video_id < ?))
			 ORDER BY publish_time DESC, video_id DESC
			 LIMIT ?`,
			authorID, cursorScore, cursorScore, cursorVideoID, limit,
		)
	} else {
		rows, err = db.QueryContext(ctx,
			`SELECT video_id, publish_time
			 FROM videos
			 WHERE author_id = ?
			 ORDER BY publish_time DESC, video_id DESC
			 LIMIT ?`,
			authorID, limit,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]timelineItem, 0, limit)
	for rows.Next() {
		var it timelineItem
		if err := rows.Scan(&it.videoID, &it.score); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return items, nil
}

func queryHomeFeed(ctx context.Context, userID string, hasCursor bool, cursorScore int64, cursorVideoID string, limit int64) ([]videoDetail, error) {
	db := mysql_client.Get()
	if db == nil {
		return nil, nil
	}

	var (
		rows *sql.Rows
		err  error
	)

	if hasCursor {
		parts := strings.SplitN(cursorVideoID, "|", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid cursor_video_id")
		}
		cursorAuthorID := parts[0]
		cursorVideo := parts[1]

		rows, err = db.QueryContext(ctx,
			`SELECT v.author_id, COALESCE(u.nickname, v.author_id), COALESCE(u.avatar_url, ''), v.video_id, v.title, v.cover_url, v.video_url, v.description, v.publish_time
			 FROM follows f
			 JOIN videos v ON v.author_id = f.target_user_id
			 LEFT JOIN users u ON u.username = v.author_id
			 WHERE f.user_id = ?
			   AND (v.publish_time < ?
			     OR (v.publish_time = ? AND (v.author_id < ? OR (v.author_id = ? AND v.video_id < ?))))
			 ORDER BY v.publish_time DESC, v.author_id DESC, v.video_id DESC
			 LIMIT ?`,
			userID, cursorScore, cursorScore, cursorAuthorID, cursorAuthorID, cursorVideo, limit,
		)
	} else {
		rows, err = db.QueryContext(ctx,
			`SELECT v.author_id, COALESCE(u.nickname, v.author_id), COALESCE(u.avatar_url, ''), v.video_id, v.title, v.cover_url, v.video_url, v.description, v.publish_time
			 FROM follows f
			 JOIN videos v ON v.author_id = f.target_user_id
			 LEFT JOIN users u ON u.username = v.author_id
			 WHERE f.user_id = ?
			 ORDER BY v.publish_time DESC, v.author_id DESC, v.video_id DESC
			 LIMIT ?`,
			userID, limit,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]videoDetail, 0, limit)
	for rows.Next() {
		var it videoDetail
		if err := rows.Scan(&it.AuthorID, &it.AuthorNickname, &it.AuthorAvatar, &it.VideoID, &it.Title, &it.CoverURL, &it.VideoURL, &it.Description, &it.PublishTime); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return items, nil
}

func loadVideoDetails(ctx context.Context, refs []videoRef) ([]videoDetail, error) {
	if len(refs) == 0 {
		return []videoDetail{}, nil
	}

	db := mysql_client.Get()
	if db == nil {
		return nil, fmt.Errorf("mysql client not initialized")
	}

	clauses := make([]string, 0, len(refs))
	args := make([]interface{}, 0, len(refs)*2)
	for _, ref := range refs {
		clauses = append(clauses, "(author_id = ? AND video_id = ?)")
		args = append(args, ref.authorID, ref.videoID)
	}

	query := `SELECT v.author_id, COALESCE(u.nickname, v.author_id), COALESCE(u.avatar_url, ''), v.video_id, v.title, v.cover_url, v.video_url, v.description, v.publish_time
		FROM videos v
		LEFT JOIN users u ON u.username = v.author_id
		WHERE ` + strings.Join(clauses, " OR ")
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	loaded := make(map[string]videoDetail, len(refs))
	for rows.Next() {
		var item videoDetail
		if err := rows.Scan(
			&item.AuthorID,
			&item.AuthorNickname,
			&item.AuthorAvatar,
			&item.VideoID,
			&item.Title,
			&item.CoverURL,
			&item.VideoURL,
			&item.Description,
			&item.PublishTime,
		); err != nil {
			return nil, err
		}
		loaded[inboxMember(item.AuthorID, item.VideoID)] = item
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]videoDetail, 0, len(refs))
	for _, ref := range refs {
		key := inboxMember(ref.authorID, ref.videoID)
		if item, ok := loaded[key]; ok {
			if item.PublishTime == 0 {
				item.PublishTime = ref.score
			}
			result = append(result, item)
			continue
		}

		result = append(result, videoDetail{
			AuthorID:    ref.authorID,
			VideoID:     ref.videoID,
			PublishTime: ref.score,
		})
	}
	return result, nil
}
