package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"videofeed/cache"
	"videofeed/mysql_client"
	"videofeed/redis_client"
)

type videoLikeRequest struct {
	AuthorID string `json:"author_id"`
	VideoID  string `json:"video_id"`
}

type videoUpdateRequest struct {
	VideoID     string `json:"video_id"`
	Description string `json:"description"`
	VideoURL    string `json:"video_url"`
	CoverURL    string `json:"cover_url"`
}

func videoKey(authorID, videoID string) string {
	return authorID + "|" + videoID
}

func attachLikeStatsToVideos(ctx context.Context, currentUser string, videos []videoDetail) error {
	if len(videos) == 0 {
		return nil
	}
	db := mysql_client.Get()
	if db == nil {
		return nil
	}

	clauses := make([]string, 0, len(videos))
	args := make([]interface{}, 0, len(videos)*2)
	for _, item := range videos {
		clauses = append(clauses, "(author_id = ? AND video_id = ?)")
		args = append(args, item.AuthorID, item.VideoID)
	}

	countRows, err := db.QueryContext(ctx,
		`SELECT author_id, video_id, COUNT(1)
		 FROM video_likes
		 WHERE `+strings.Join(clauses, " OR ")+`
		 GROUP BY author_id, video_id`, args...)
	if err != nil {
		return err
	}
	defer countRows.Close()

	counts := make(map[string]int64, len(videos))
	for countRows.Next() {
		var authorID, videoID string
		var likeCount int64
		if err := countRows.Scan(&authorID, &videoID, &likeCount); err != nil {
			return err
		}
		counts[videoKey(authorID, videoID)] = likeCount
	}
	if err := countRows.Err(); err != nil {
		return err
	}

	likedSet := make(map[string]struct{})
	if strings.TrimSpace(currentUser) != "" {
		likedArgs := make([]interface{}, 0, 1+len(videos)*2)
		likedArgs = append(likedArgs, currentUser)
		likedArgs = append(likedArgs, args...)
		likedRows, err := db.QueryContext(ctx,
			`SELECT author_id, video_id
			 FROM video_likes
			 WHERE user_id = ?
			   AND (`+strings.Join(clauses, " OR ")+`)`, likedArgs...)
		if err != nil {
			return err
		}
		defer likedRows.Close()

		for likedRows.Next() {
			var authorID, videoID string
			if err := likedRows.Scan(&authorID, &videoID); err != nil {
				return err
			}
			likedSet[videoKey(authorID, videoID)] = struct{}{}
		}
		if err := likedRows.Err(); err != nil {
			return err
		}
	}

	// Also attach comment counts
	commentRows, err := db.QueryContext(ctx,
		`SELECT author_id, video_id, COUNT(1)
		 FROM video_comments
		 WHERE `+strings.Join(clauses, " OR ")+`
		 GROUP BY author_id, video_id`, args...)
	commentCounts := make(map[string]int64)
	if err == nil {
		defer commentRows.Close()
		for commentRows.Next() {
			var authorID, videoID string
			var cc int64
			if err := commentRows.Scan(&authorID, &videoID, &cc); err == nil {
				commentCounts[videoKey(authorID, videoID)] = cc
			}
		}
	}

	// Also attach favorite count and status
	favCounts := make(map[string]int64)
	favSet := make(map[string]struct{})
	if strings.TrimSpace(currentUser) != "" {
		favArgs := make([]interface{}, 0, 1+len(videos)*2)
		favArgs = append(favArgs, currentUser)
		favArgs = append(favArgs, args...)
		favRows, err := db.QueryContext(ctx,
			`SELECT author_id, video_id
			 FROM video_favorites
			 WHERE user_id = ?
			   AND (`+strings.Join(clauses, " OR ")+`)`, favArgs...)
		if err == nil {
			defer favRows.Close()
			for favRows.Next() {
				var authorID, videoID string
				if err := favRows.Scan(&authorID, &videoID); err == nil {
					favSet[videoKey(authorID, videoID)] = struct{}{}
				}
			}
		}
	}
	// Count total favorites per video (regardless of current user)
	favCountRows, err := db.QueryContext(ctx,
		`SELECT author_id, video_id, COUNT(1)
		 FROM video_favorites
		 WHERE `+strings.Join(clauses, " OR ")+`
		 GROUP BY author_id, video_id`, args...)
	if err == nil {
		defer favCountRows.Close()
		for favCountRows.Next() {
			var authorID, videoID string
			var fc int64
			if err := favCountRows.Scan(&authorID, &videoID, &fc); err == nil {
				favCounts[videoKey(authorID, videoID)] = fc
			}
		}
	}

	for i := range videos {
		key := videoKey(videos[i].AuthorID, videos[i].VideoID)
		videos[i].LikeCount = counts[key]
		_, videos[i].Liked = likedSet[key]
		videos[i].CommentCount = commentCounts[key]
		_, videos[i].Favorited = favSet[key]
		videos[i].FavoriteCount = favCounts[key]
	}
	return nil
}

func attachLikeStatsToVideoCards(ctx context.Context, authorID string, cards []accountVideoCard) error {
	if len(cards) == 0 {
		return nil
	}
	db := mysql_client.Get()
	if db == nil {
		return nil
	}

	placeholders := make([]string, 0, len(cards))
	args := make([]interface{}, 0, 1+len(cards))
	args = append(args, authorID)
	for _, item := range cards {
		placeholders = append(placeholders, "?")
		args = append(args, item.VideoID)
	}

	rows, err := db.QueryContext(ctx,
		`SELECT video_id, COUNT(1)
		 FROM video_likes
		 WHERE author_id = ?
		   AND video_id IN (`+strings.Join(placeholders, ",")+`)
		 GROUP BY video_id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	counts := make(map[string]int64, len(cards))
	for rows.Next() {
		var videoID string
		var likeCount int64
		if err := rows.Scan(&videoID, &likeCount); err != nil {
			return err
		}
		counts[videoID] = likeCount
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for i := range cards {
		cards[i].LikeCount = counts[cards[i].VideoID]
	}
	return nil
}

func loadVideoDetailByID(ctx context.Context, authorID, videoID, currentUser string) (videoDetail, error) {
	db := mysql_client.Get()
	if db == nil {
		return videoDetail{}, fmt.Errorf("mysql client not initialized")
	}

	var item videoDetail
	err := db.QueryRowContext(ctx,
		`SELECT v.author_id, COALESCE(u.nickname, v.author_id), COALESCE(u.avatar_url, ''), v.video_id, v.title, v.cover_url, v.video_url, v.description, v.publish_time
		 FROM videos v
		 LEFT JOIN users u ON u.username = v.author_id
		 WHERE v.author_id = ? AND v.video_id = ?`,
		authorID, videoID,
	).Scan(
		&item.AuthorID,
		&item.AuthorNickname,
		&item.AuthorAvatar,
		&item.VideoID,
		&item.Title,
		&item.CoverURL,
		&item.VideoURL,
		&item.Description,
		&item.PublishTime,
	)
	if err != nil {
		return videoDetail{}, err
	}
	items := []videoDetail{item}
	if err := attachLikeStatsToVideos(ctx, currentUser, items); err != nil {
		return videoDetail{}, err
	}
	return items[0], nil
}

func VideoDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Code: 1, Msg: "method not allowed"})
		return
	}

	authorID := strings.TrimSpace(r.URL.Query().Get("author_id"))
	videoID := strings.TrimSpace(r.URL.Query().Get("video_id"))
	if authorID == "" || videoID == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "missing author_id or video_id"})
		return
	}

	currentUser, _ := currentUsername(r)

	// 缓存击穿防护：热点视频用 singleflight 合并并发请求
	// key 格式：cache:video:{author_id}:{video_id}
	cacheKey := fmt.Sprintf("cache:video:%s:%s", authorID, videoID)
	detailJSON, err := cache.Fetch(r.Context(), cacheKey, 30*time.Second, func() (string, error) {
		detail, err := loadVideoDetailByID(r.Context(), authorID, videoID, currentUser)
		if err != nil {
			if err == sql.ErrNoRows {
				// 视频不存在，返回空字符串，cache.Fetch 会写入 EMPTY_DB
				return "", nil
			}
			return "", err
		}
		b, _ := json.Marshal(detail)
		return string(b), nil
	})
	if err != nil {
		if err == cache.ErrNotFound {
			writeJSON(w, http.StatusNotFound, apiResponse{Code: 1, Msg: "video not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "load video detail failed"})
		return
	}

	var detail videoDetail
	_ = json.Unmarshal([]byte(detailJSON), &detail)
	writeJSON(w, http.StatusOK, apiResponse{Code: 0, Data: detail})
}

func LikeVideo(w http.ResponseWriter, r *http.Request) {
	handleVideoLikeMutation(w, r, true)
}

func UnlikeVideo(w http.ResponseWriter, r *http.Request) {
	handleVideoLikeMutation(w, r, false)
}

func handleVideoLikeMutation(w http.ResponseWriter, r *http.Request, like bool) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Code: 1, Msg: "method not allowed"})
		return
	}

	userID, err := requireCurrentUsername(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, apiResponse{Code: 1, Msg: "login required"})
		return
	}

	var req videoLikeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "invalid json"})
		return
	}
	req.AuthorID = strings.TrimSpace(req.AuthorID)
	req.VideoID = strings.TrimSpace(req.VideoID)
	if req.AuthorID == "" || req.VideoID == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "missing author_id or video_id"})
		return
	}

	db := mysql_client.Get()
	if db == nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "mysql client not initialized"})
		return
	}

	if like {
		if _, err := db.ExecContext(r.Context(),
			`INSERT INTO video_likes (user_id, author_id, video_id)
			 VALUES (?, ?, ?)
			 ON DUPLICATE KEY UPDATE created_at = created_at`,
			userID, req.AuthorID, req.VideoID,
		); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "like video failed"})
			return
		}
	} else {
		if _, err := db.ExecContext(r.Context(),
			`DELETE FROM video_likes
			 WHERE user_id = ? AND author_id = ? AND video_id = ?`,
			userID, req.AuthorID, req.VideoID,
		); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "unlike video failed"})
			return
		}
	}

	// 互动后更新热榜分数（MySQL 统计计数 → Redis ZSet）
	updateVideoHotScore(r.Context(), req.AuthorID, req.VideoID)

	detail, err := loadVideoDetailByID(r.Context(), req.AuthorID, req.VideoID, userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "reload video detail failed"})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Code: 0, Data: detail})
}

func UpdateVideo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Code: 1, Msg: "method not allowed"})
		return
	}

	userID, err := requireCurrentUsername(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, apiResponse{Code: 1, Msg: "login required"})
		return
	}

	var req videoUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "invalid json"})
		return
	}
	req.VideoID = strings.TrimSpace(req.VideoID)
	req.Description = strings.TrimSpace(req.Description)
	req.VideoURL = strings.TrimSpace(req.VideoURL)
	req.CoverURL = strings.TrimSpace(req.CoverURL)
	if req.VideoID == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "missing video_id"})
		return
	}

	db := mysql_client.Get()
	if db == nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "mysql client not initialized"})
		return
	}

	var exists int
	if err := db.QueryRowContext(r.Context(),
		`SELECT COUNT(1)
		 FROM videos
		 WHERE author_id = ? AND video_id = ?`,
		userID, req.VideoID,
	).Scan(&exists); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "check video failed"})
		return
	}
	if exists == 0 {
		writeJSON(w, http.StatusNotFound, apiResponse{Code: 1, Msg: "video not found"})
		return
	}

	if _, err := db.ExecContext(r.Context(),
		`UPDATE videos
		 SET description = ?,
		     video_url = CASE WHEN ? = '' THEN video_url ELSE ? END,
		     cover_url = CASE WHEN ? = '' THEN cover_url ELSE ? END
		 WHERE author_id = ? AND video_id = ?`,
		req.Description, req.VideoURL, req.VideoURL, req.CoverURL, req.CoverURL, userID, req.VideoID,
	); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "update video failed"})
		return
	}

	detail, err := loadVideoDetailByID(r.Context(), userID, req.VideoID, userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "reload updated video failed"})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Code: 0, Msg: "success", Data: detail})
}

func DeleteVideo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Code: 1, Msg: "method not allowed"})
		return
	}

	userID, err := requireCurrentUsername(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, apiResponse{Code: 1, Msg: "login required"})
		return
	}

	var req videoLikeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "invalid json"})
		return
	}
	req.VideoID = strings.TrimSpace(req.VideoID)
	if req.VideoID == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "missing video_id"})
		return
	}

	db := mysql_client.Get()
	if db == nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "mysql client not initialized"})
		return
	}

	res, err := db.ExecContext(r.Context(),
		`DELETE FROM videos
		 WHERE author_id = ? AND video_id = ?`,
		userID, req.VideoID,
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "delete video failed"})
		return
	}
	affected, err := res.RowsAffected()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "delete video failed"})
		return
	}
	if affected == 0 {
		writeJSON(w, http.StatusNotFound, apiResponse{Code: 1, Msg: "video not found"})
		return
	}

	if _, err := db.ExecContext(r.Context(),
		`DELETE FROM video_likes
		 WHERE author_id = ? AND video_id = ?`,
		userID, req.VideoID,
	); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "clear video likes failed"})
		return
	}

	if c := redis_client.Get(); c != nil {
		member := inboxMember(userID, req.VideoID)
		_, _ = c.TxPipelined(r.Context(), func(p redis.Pipeliner) error {
			p.ZRem(r.Context(), "feed:timeline:"+userID, req.VideoID)
			p.ZRem(r.Context(), inboxKey(userID), member)
			p.ZRem(r.Context(), hotBucketKey(), member)
			p.ZRem(r.Context(), HotMergeResultKey, member)
			return nil
		})
		if followers, err := loadFollowers(r.Context(), userID); err == nil && len(followers) > 0 {
			_, _ = c.TxPipelined(r.Context(), func(p redis.Pipeliner) error {
				for _, followerID := range followers {
					p.ZRem(r.Context(), inboxKey(followerID), member)
				}
				return nil
			})
		}
	}

	writeJSON(w, http.StatusOK, apiResponse{Code: 0, Msg: "success"})
}

// ---- comment ----

type commentItem struct {
	ID        int64  `json:"id"`
	UserID    string `json:"user_id"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
}

func CommentVideo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Code: 1, Msg: "method not allowed"})
		return
	}
	userID, err := requireCurrentUsername(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, apiResponse{Code: 1, Msg: "login required"})
		return
	}

	var req struct {
		AuthorID string `json:"author_id"`
		VideoID  string `json:"video_id"`
		Content  string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "invalid json"})
		return
	}
	req.AuthorID = strings.TrimSpace(req.AuthorID)
	req.VideoID = strings.TrimSpace(req.VideoID)
	req.Content = strings.TrimSpace(req.Content)
	if req.AuthorID == "" || req.VideoID == "" || req.Content == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "missing author_id, video_id or content"})
		return
	}

	db := mysql_client.Get()
	if db == nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "mysql client not initialized"})
		return
	}

	res, err := db.ExecContext(r.Context(),
		`INSERT INTO video_comments (author_id, video_id, user_id, content) VALUES (?, ?, ?, ?)`,
		req.AuthorID, req.VideoID, userID, req.Content,
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "comment failed"})
		return
	}
	id, _ := res.LastInsertId()

	// 评论后更新热榜分数
	updateVideoHotScore(r.Context(), req.AuthorID, req.VideoID)

	writeJSON(w, http.StatusOK, apiResponse{Code: 0, Data: commentItem{
		ID:      id,
		UserID:  userID,
		Content: req.Content,
	}})
}

func ListComments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Code: 1, Msg: "method not allowed"})
		return
	}
	authorID := strings.TrimSpace(r.URL.Query().Get("author_id"))
	videoID := strings.TrimSpace(r.URL.Query().Get("video_id"))
	if authorID == "" || videoID == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "missing author_id or video_id"})
		return
	}

	db := mysql_client.Get()
	if db == nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "mysql client not initialized"})
		return
	}

	comments := make([]commentItem, 0)
	rows, err := db.QueryContext(r.Context(),
		`SELECT id, user_id, content, created_at
		 FROM video_comments
		 WHERE author_id = ? AND video_id = ?
		 ORDER BY created_at DESC
		 LIMIT 100`,
		authorID, videoID,
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "load comments failed"})
		return
	}
	defer rows.Close()
	for rows.Next() {
		var c commentItem
		var ts string
		if err := rows.Scan(&c.ID, &c.UserID, &c.Content, &ts); err != nil {
			continue
		}
		c.CreatedAt = ts
		comments = append(comments, c)
	}

	writeJSON(w, http.StatusOK, apiResponse{Code: 0, Data: comments})
}

func DeleteComment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Code: 1, Msg: "method not allowed"})
		return
	}
	userID, err := requireCurrentUsername(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, apiResponse{Code: 1, Msg: "login required"})
		return
	}

	var req struct {
		CommentID int64 `json:"comment_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "invalid json"})
		return
	}
	if req.CommentID <= 0 {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "missing comment_id"})
		return
	}

	db := mysql_client.Get()
	if db == nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "mysql client not initialized"})
		return
	}

	res, err := db.ExecContext(r.Context(),
		`DELETE FROM video_comments WHERE id = ? AND user_id = ?`,
		req.CommentID, userID,
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "delete comment failed"})
		return
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		writeJSON(w, http.StatusNotFound, apiResponse{Code: 1, Msg: "comment not found"})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Code: 0, Msg: "success"})
}

// ---- favorite ----

func FavoriteVideo(w http.ResponseWriter, r *http.Request) {
	handleVideoFavoriteMutation(w, r, true)
}

func UnfavoriteVideo(w http.ResponseWriter, r *http.Request) {
	handleVideoFavoriteMutation(w, r, false)
}

func handleVideoFavoriteMutation(w http.ResponseWriter, r *http.Request, fav bool) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Code: 1, Msg: "method not allowed"})
		return
	}
	userID, err := requireCurrentUsername(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, apiResponse{Code: 1, Msg: "login required"})
		return
	}

	var req videoLikeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "invalid json"})
		return
	}
	req.AuthorID = strings.TrimSpace(req.AuthorID)
	req.VideoID = strings.TrimSpace(req.VideoID)
	if req.AuthorID == "" || req.VideoID == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "missing author_id or video_id"})
		return
	}

	db := mysql_client.Get()
	if db == nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "mysql client not initialized"})
		return
	}

	if fav {
		if _, err := db.ExecContext(r.Context(),
			`INSERT INTO video_favorites (user_id, author_id, video_id)
			 VALUES (?, ?, ?)
			 ON DUPLICATE KEY UPDATE created_at = created_at`,
			userID, req.AuthorID, req.VideoID,
		); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "favorite video failed"})
			return
		}
	} else {
		if _, err := db.ExecContext(r.Context(),
			`DELETE FROM video_favorites
			 WHERE user_id = ? AND author_id = ? AND video_id = ?`,
			userID, req.AuthorID, req.VideoID,
		); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "unfavorite video failed"})
			return
		}
	}

	// 收藏/取消收藏后更新热榜分数
	updateVideoHotScore(r.Context(), req.AuthorID, req.VideoID)

	detail, err := loadVideoDetailByID(r.Context(), req.AuthorID, req.VideoID, userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "reload video detail failed"})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Code: 0, Data: detail})
}

func ListFavorites(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Code: 1, Msg: "method not allowed"})
		return
	}

	userID, err := requireCurrentUsername(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, apiResponse{Code: 1, Msg: "login required"})
		return
	}

	db := mysql_client.Get()
	if db == nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "mysql client not initialized"})
		return
	}

	type favItem struct {
		AuthorID string `json:"author_id"`
		VideoID  string `json:"video_id"`
	}
	items := make([]favItem, 0)
	rows, err := db.QueryContext(r.Context(),
		`SELECT author_id, video_id
		 FROM video_favorites
		 WHERE user_id = ?
		 ORDER BY created_at DESC`,
		userID,
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "load favorites failed"})
		return
	}
	defer rows.Close()
	for rows.Next() {
		var item favItem
		if err := rows.Scan(&item.AuthorID, &item.VideoID); err != nil {
			continue
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, apiResponse{Code: 0, Data: items})
}

func ListLikes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Code: 1, Msg: "method not allowed"})
		return
	}

	userID, err := requireCurrentUsername(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, apiResponse{Code: 1, Msg: "login required"})
		return
	}

	db := mysql_client.Get()
	if db == nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "mysql client not initialized"})
		return
	}

	type likeItem struct {
		AuthorID string `json:"author_id"`
		VideoID  string `json:"video_id"`
	}
	items := make([]likeItem, 0)
	rows, err := db.QueryContext(r.Context(),
		`SELECT author_id, video_id
		 FROM video_likes
		 WHERE user_id = ?
		 ORDER BY created_at DESC`,
		userID,
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "load likes failed"})
		return
	}
	defer rows.Close()
	for rows.Next() {
		var item likeItem
		if err := rows.Scan(&item.AuthorID, &item.VideoID); err != nil {
			continue
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, apiResponse{Code: 0, Data: items})
}
