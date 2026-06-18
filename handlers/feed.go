package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"

	"videofeed/redis_client"
)

type followRequest struct {
	UserID       string `json:"user_id"`
	TargetUserID string `json:"target_user_id"`
}

type homeFeedEntry struct {
	AuthorID    string `json:"author_id"`
	VideoID     string `json:"video_id"`
	PublishTime int64  `json:"publish_time"`
}

func followingKey(userID string) string { return "relation:following:" + userID }
func followersKey(userID string) string { return "relation:followers:" + userID }

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

	c := redis_client.Get()
	if c == nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "redis client not initialized"})
		return
	}

	key := "feed:timeline:" + userID

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
	type item struct {
		videoID string
		score   int64
	}
	collected := make([]item, 0, limit+1)
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
			writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "redis zrevrangebyscore failed"})
			return
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

			collected = append(collected, item{videoID: member, score: score})
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

	hasMore := len(collected) > int(limit)
	if hasMore {
		collected = collected[:limit]
	}

	videoIDs := make([]string, 0, len(collected))
	for _, it := range collected {
		videoIDs = append(videoIDs, it.videoID)
	}

	var nextCursor *cursorToken
	if hasMore && len(collected) > 0 {
		last := collected[len(collected)-1]
		nextCursor = &cursorToken{Score: last.score, VideoID: last.videoID}
	}

	// 注意：我们只存 video_id（member），不存视频详情。MVP 只需要能返回视频ID列表即可。
	writeJSON(w, http.StatusOK, apiResponse{Code: 0, Data: videoIDs, NextCursor: nextCursor})
}

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

	c := redis_client.Get()
	if c == nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "redis client not initialized"})
		return
	}

	_, err := c.TxPipelined(r.Context(), func(p redis.Pipeliner) error {
		p.SAdd(r.Context(), followingKey(req.UserID), req.TargetUserID)
		p.SAdd(r.Context(), followersKey(req.TargetUserID), req.UserID)
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "redis sadd failed"})
		return
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

	c := redis_client.Get()
	if c == nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "redis client not initialized"})
		return
	}

	_, err := c.TxPipelined(r.Context(), func(p redis.Pipeliner) error {
		p.SRem(r.Context(), followingKey(req.UserID), req.TargetUserID)
		p.SRem(r.Context(), followersKey(req.TargetUserID), req.UserID)
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "redis srem failed"})
		return
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

	c := redis_client.Get()
	if c == nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "redis client not initialized"})
		return
	}

	users, err := c.SMembers(r.Context(), followingKey(userID)).Result()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "redis smembers failed"})
		return
	}
	sort.Strings(users)
	writeJSON(w, http.StatusOK, apiResponse{Code: 0, Data: users})
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

	c := redis_client.Get()
	if c == nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "redis client not initialized"})
		return
	}

	following, err := c.SMembers(r.Context(), followingKey(userID)).Result()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "redis smembers failed"})
		return
	}
	if len(following) == 0 {
		writeJSON(w, http.StatusOK, apiResponse{Code: 0, Data: []homeFeedEntry{}})
		return
	}

	sort.Strings(following)
	if len(following) > 200 {
		following = following[:200]
	}

	perAuthor := int64(50)
	if perAuthor < limit*3 {
		perAuthor = limit * 3
	}
	if perAuthor > 200 {
		perAuthor = 200
	}

	type candidate struct {
		authorID string
		videoID  string
		score    int64
	}
	cands := make([]candidate, 0, int(limit)+1)

	max := "+inf"
	if hasCursor {
		max = strconv.FormatInt(cursorScore, 10)
	}

	for _, authorID := range following {
		key := "feed:timeline:" + authorID
		res, e := c.ZRevRangeByScoreWithScores(r.Context(), key, &redis.ZRangeBy{
			Max:    max,
			Min:    "-inf",
			Offset: 0,
			Count:  perAuthor,
		}).Result()
		if e != nil {
			writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "redis zrevrangebyscore failed"})
			return
		}
		for _, z := range res {
			score := int64(z.Score)
			videoID := fmt.Sprint(z.Member)

			if hasCursor {
				if score > cursorScore {
					continue
				}
				if score == cursorScore && videoID >= cursorVideoID {
					continue
				}
			}

			cands = append(cands, candidate{authorID: authorID, videoID: videoID, score: score})
		}
	}

	sort.Slice(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		if cands[i].videoID != cands[j].videoID {
			return cands[i].videoID > cands[j].videoID
		}
		return cands[i].authorID > cands[j].authorID
	})

	hasMore := int64(len(cands)) > limit
	if hasMore {
		cands = cands[:limit]
	}

	out := make([]homeFeedEntry, 0, len(cands))
	for _, it := range cands {
		out = append(out, homeFeedEntry{
			AuthorID:    it.authorID,
			VideoID:     it.videoID,
			PublishTime: it.score,
		})
	}

	var nextCursor *cursorToken
	if hasMore && len(cands) > 0 {
		last := cands[len(cands)-1]
		nextCursor = &cursorToken{Score: last.score, VideoID: last.videoID}
	}

	writeJSON(w, http.StatusOK, apiResponse{Code: 0, Data: out, NextCursor: nextCursor})
}
