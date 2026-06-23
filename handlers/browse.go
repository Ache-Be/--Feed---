package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"videofeed/mysql_client"
	"videofeed/redis_client"
)

type accountVideoCard struct {
	VideoID     string `json:"video_id"`
	CoverURL    string `json:"cover_url"`
	LikeCount   int64  `json:"like_count"`
	PublishTime int64  `json:"publish_time"`
}

func loadAccountVideoCards(ctx context.Context, authorID string, limit int64) ([]accountVideoCard, error) {
	db := mysql_client.Get()
	if db == nil {
		return nil, nil
	}

	rows, err := db.QueryContext(ctx, `SELECT video_id, COALESCE(cover_url, ''), publish_time
		FROM videos
		WHERE author_id = ?
		ORDER BY publish_time DESC, video_id DESC
		LIMIT ?`, authorID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]accountVideoCard, 0, limit)
	for rows.Next() {
		var item accountVideoCard
		if err := rows.Scan(&item.VideoID, &item.CoverURL, &item.PublishTime); err != nil {
			return nil, err
		}
		// 点赞能力尚未接入，这里先保留展示字段，默认返回 0。
		item.LikeCount = 0
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := attachLikeStatsToVideoCards(ctx, authorID, items); err != nil {
		return nil, err
	}
	return items, nil
}

func Recommend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Code: 1, Msg: "method not allowed"})
		return
	}

	videos, err := queryBrowseVideos(r, false)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "query recommend failed"})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Code: 0, Data: videos})
}

func Hot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Code: 1, Msg: "method not allowed"})
		return
	}

	q := strings.TrimSpace(r.URL.Query().Get("q"))

	limit := int64(20)
	if s := r.URL.Query().Get("limit"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err == nil && v > 0 && v <= 100 {
			limit = v
		}
	}

	// 有搜索关键词 → 不走 Redis，直接用 MySQL 搜索 + 权重排序
	if q != "" {
		videos, err := queryHotVideosFromMySQL(r, q, limit)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "query hot failed"})
			return
		}
		writeJSON(w, http.StatusOK, apiResponse{Code: 0, Data: videos})
		return
	}

	// 无搜索 → 优先走 Redis ZSet 热榜（权重已预先计算好）
	c := redis_client.Get()
	if c != nil {
		zs, err := c.ZRevRangeWithScores(r.Context(), hotRankKey(), 0, limit-1).Result()
		if err == nil && len(zs) > 0 {
			videoRefs := make([]videoRef, 0, len(zs))
			for _, z := range zs {
				member := fmt.Sprint(z.Member)
				parts := strings.SplitN(member, "|", 2)
				if len(parts) == 2 {
					videoRefs = append(videoRefs, videoRef{
						authorID: parts[0],
						videoID:  parts[1],
						score:    int64(z.Score),
					})
				}
			}
			videos, err := loadVideoDetails(r.Context(), videoRefs)
			if err == nil {
				currentUser, _ := currentUsername(r)
				_ = attachLikeStatsToVideos(r.Context(), currentUser, videos)
				writeJSON(w, http.StatusOK, apiResponse{Code: 0, Data: videos})
				return
			}
		}
	}

	// Redis 挂了 or ZSet 为空 → MySQL 降级
	videos, err := queryHotVideosFromMySQL(r, "", limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "query hot failed"})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Code: 0, Data: videos})
}

// queryHotVideosFromMySQL 用 MySQL 子查询计算权重分排序（Redis 不可用时的降级方案）。
//
// 降级时用子查询实时算分，保证数据正确性：
//
//	(SELECT COUNT FROM video_likes) * 10 +
//	(SELECT COUNT FROM video_favorites) * 5 +
//	(SELECT COUNT FROM video_comments) * 3
//
// 作为 ORDER BY 的排序依据。
func queryHotVideosFromMySQL(r *http.Request, q string, limit int64) ([]videoDetail, error) {
	db := mysql_client.Get()
	if db == nil {
		return nil, nil
	}

	pattern := "%" + q + "%"
	rows, err := db.QueryContext(r.Context(), `
		SELECT
			v.author_id,
			COALESCE(u.nickname, v.author_id),
			COALESCE(u.avatar_url, ''),
			v.video_id,
			v.title,
			v.cover_url,
			v.video_url,
			v.description,
			v.publish_time
		FROM videos v
		LEFT JOIN users u ON u.username = v.author_id
		WHERE (? = '' OR v.title LIKE ? OR v.description LIKE ? OR v.author_id LIKE ? OR COALESCE(u.nickname, v.author_id) LIKE ?)
		ORDER BY
			(SELECT COUNT(1) FROM video_likes    WHERE author_id = v.author_id AND video_id = v.video_id) * 10 +
			(SELECT COUNT(1) FROM video_favorites WHERE author_id = v.author_id AND video_id = v.video_id) * 5 +
			(SELECT COUNT(1) FROM video_comments WHERE author_id = v.author_id AND video_id = v.video_id) * 3 DESC,
			v.publish_time DESC
		LIMIT ?`,
		q, pattern, pattern, pattern, pattern, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]videoDetail, 0, limit)
	for rows.Next() {
		var item videoDetail
		if err := rows.Scan(
			&item.AuthorID, &item.AuthorNickname, &item.AuthorAvatar,
			&item.VideoID, &item.Title, &item.CoverURL, &item.VideoURL,
			&item.Description, &item.PublishTime,
		); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	currentUser, _ := currentUsername(r)
	if err := attachLikeStatsToVideos(r.Context(), currentUser, items); err != nil {
		return nil, err
	}
	return items, nil
}

func MyVideos(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Code: 1, Msg: "method not allowed"})
		return
	}

	username, err := requireCurrentUsername(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, apiResponse{Code: 1, Msg: "login required"})
		return
	}

	db := mysql_client.Get()
	if db == nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "mysql client not initialized"})
		return
	}

	items, err := loadAccountVideoCards(r.Context(), username, 60)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "query my videos failed"})
		return
	}

	writeJSON(w, http.StatusOK, apiResponse{Code: 0, Data: items})
}

func queryBrowseVideos(r *http.Request, hot bool) ([]videoDetail, error) {
	db := mysql_client.Get()
	if db == nil {
		return nil, nil
	}

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := int64(20)
	pattern := "%" + q + "%"

	var (
		rows *sql.Rows
		err  error
	)
	if hot {
		rows, err = db.QueryContext(r.Context(), `SELECT
				v.author_id,
				COALESCE(u.nickname, v.author_id),
				COALESCE(u.avatar_url, ''),
				v.video_id,
				v.title,
				v.cover_url,
				v.video_url,
				v.description,
				v.publish_time
			FROM videos v
			LEFT JOIN users u ON u.username = v.author_id
			LEFT JOIN follows f ON f.target_user_id = v.author_id
			WHERE (? = '' OR v.title LIKE ? OR v.description LIKE ? OR v.author_id LIKE ? OR COALESCE(u.nickname, v.author_id) LIKE ?)
			GROUP BY v.author_id, u.nickname, u.avatar_url, v.video_id, v.title, v.cover_url, v.video_url, v.description, v.publish_time
			ORDER BY COUNT(f.user_id) DESC, v.publish_time DESC, v.author_id DESC, v.video_id DESC
			LIMIT ?`, q, pattern, pattern, pattern, pattern, limit)
	} else {
		rows, err = db.QueryContext(r.Context(), `SELECT
				v.author_id,
				COALESCE(u.nickname, v.author_id),
				COALESCE(u.avatar_url, ''),
				v.video_id,
				v.title,
				v.cover_url,
				v.video_url,
				v.description,
				v.publish_time
			FROM videos v
			LEFT JOIN users u ON u.username = v.author_id
			WHERE (? = '' OR v.title LIKE ? OR v.description LIKE ? OR v.author_id LIKE ? OR COALESCE(u.nickname, v.author_id) LIKE ?)
			ORDER BY v.publish_time DESC, v.author_id DESC, v.video_id DESC
			LIMIT ?`, q, pattern, pattern, pattern, pattern, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]videoDetail, 0, limit)
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
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	currentUser, _ := currentUsername(r)
	if err := attachLikeStatsToVideos(r.Context(), currentUser, items); err != nil {
		return nil, err
	}
	return items, nil
}
