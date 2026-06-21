package handlers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"videofeed/mysql_client"
)

const sessionCookieName = "vf_session"

var sessionStore = struct {
	sync.RWMutex
	byToken map[string]string
}{
	byToken: make(map[string]string),
}

type accountRequest struct {
	Username         string `json:"username"`
	Password         string `json:"password"`
	Nickname         string `json:"nickname"`
	AvatarURL        string `json:"avatar_url"`
	Age              *int   `json:"age,omitempty"`
	Address          string `json:"address"`
	Signature        string `json:"signature"`
	SecurityQuestion string `json:"security_question"`
	SecurityAnswer   string `json:"security_answer"`
}

type resetPasswordRequest struct {
	Username       string `json:"username"`
	SecurityAnswer string `json:"security_answer"`
}

type accountProfile struct {
	Username         string `json:"username"`
	Nickname         string `json:"nickname"`
	AvatarURL        string `json:"avatar_url,omitempty"`
	Age              *int   `json:"age,omitempty"`
	Address          string `json:"address,omitempty"`
	Signature        string `json:"signature,omitempty"`
	SecurityQuestion string `json:"security_question,omitempty"`
}

type publicAccountProfile struct {
	Username       string             `json:"username"`
	Nickname       string             `json:"nickname"`
	AvatarURL      string             `json:"avatar_url,omitempty"`
	Age            *int               `json:"age,omitempty"`
	Address        string             `json:"address,omitempty"`
	Signature      string             `json:"signature,omitempty"`
	FollowingCount int64              `json:"following_count"`
	FollowerCount  int64              `json:"follower_count"`
	LikeCount      int64              `json:"like_count"`
	Videos         []accountVideoCard `json:"videos"`
}

type followingProfileItem struct {
	Username  string `json:"username"`
	Nickname  string `json:"nickname"`
	AvatarURL string `json:"avatar_url,omitempty"`
}

type changePasswordRequest struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

func Register(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Code: 1, Msg: "method not allowed"})
		return
	}

	var req accountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "invalid json"})
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	req.Password = strings.TrimSpace(req.Password)
	req.Nickname = strings.TrimSpace(req.Nickname)
	req.AvatarURL = strings.TrimSpace(req.AvatarURL)
	req.Address = strings.TrimSpace(req.Address)
	req.Signature = strings.TrimSpace(req.Signature)
	req.SecurityQuestion = strings.TrimSpace(req.SecurityQuestion)
	req.SecurityAnswer = strings.TrimSpace(req.SecurityAnswer)
	if len(req.Username) < 3 || len(req.Password) < 6 || req.Nickname == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "username/password too short or missing nickname"})
		return
	}
	if req.SecurityQuestion == "" {
		req.SecurityQuestion = "默认问题：请输入默认答案 123456"
	}
	if req.SecurityAnswer == "" {
		req.SecurityAnswer = "123456"
	}

	db := mysql_client.Get()
	if db == nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "mysql client not initialized"})
		return
	}

	if _, err := db.ExecContext(r.Context(),
		`INSERT INTO users (username, password_hash, nickname, avatar_url, age, address, signature, security_question, security_answer_hash)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.Username,
		hashPassword(req.Password),
		req.Nickname,
		req.AvatarURL,
		nullableAgeValue(req.Age),
		req.Address,
		req.Signature,
		req.SecurityQuestion,
		hashPassword(req.SecurityAnswer),
	); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "username already exists"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "register failed"})
		return
	}

	writeJSON(w, http.StatusOK, apiResponse{
		Code: 0,
		Msg:  "register success",
		Data: accountProfile{
			Username:         req.Username,
			Nickname:         req.Nickname,
			AvatarURL:        req.AvatarURL,
			Age:              req.Age,
			Address:          req.Address,
			Signature:        req.Signature,
			SecurityQuestion: req.SecurityQuestion,
		},
	})
}

func Login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Code: 1, Msg: "method not allowed"})
		return
	}

	var req accountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "invalid json"})
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	req.Password = strings.TrimSpace(req.Password)
	if req.Username == "" || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "missing username or password"})
		return
	}

	db := mysql_client.Get()
	if db == nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "mysql client not initialized"})
		return
	}

	profile, passwordHash, err := loadAccountProfileWithPassword(r.Context(), db, req.Username)
	if err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "user not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "login failed"})
		return
	}
	if passwordHash != hashPassword(req.Password) {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "invalid password"})
		return
	}

	token, err := newSessionToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "create session failed"})
		return
	}

	sessionStore.Lock()
	sessionStore.byToken[token] = req.Username
	sessionStore.Unlock()
	setSessionCookie(w, token)

	writeJSON(w, http.StatusOK, apiResponse{
		Code: 0,
		Msg:  "login success",
		Data: profile,
	})
}

func Logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Code: 1, Msg: "method not allowed"})
		return
	}

	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		sessionStore.Lock()
		delete(sessionStore.byToken, cookie.Value)
		sessionStore.Unlock()
	}
	clearSessionCookie(w)
	writeJSON(w, http.StatusOK, apiResponse{Code: 0, Msg: "logout success"})
}

func Me(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Code: 1, Msg: "method not allowed"})
		return
	}

	username, ok := currentUsername(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, apiResponse{Code: 1, Msg: "not logged in"})
		return
	}

	db := mysql_client.Get()
	if db == nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "mysql client not initialized"})
		return
	}

	profile, _, err := loadAccountProfileWithPassword(r.Context(), db, username)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "load profile failed"})
		return
	}

	writeJSON(w, http.StatusOK, apiResponse{Code: 0, Data: profile})
}

func FollowingProfiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Code: 1, Msg: "method not allowed"})
		return
	}

	userID := strings.TrimSpace(r.URL.Query().Get("user_id"))
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "missing user_id"})
		return
	}

	db := mysql_client.Get()
	if db == nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "mysql client not initialized"})
		return
	}

	rows, err := db.QueryContext(r.Context(), `SELECT f.target_user_id, COALESCE(u.nickname, f.target_user_id), COALESCE(u.avatar_url, '')
		FROM follows f
		LEFT JOIN users u ON u.username = f.target_user_id
		WHERE f.user_id = ?
		ORDER BY f.target_user_id ASC`, userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "load following profiles failed"})
		return
	}
	defer rows.Close()

	items := make([]followingProfileItem, 0, 16)
	for rows.Next() {
		var item followingProfileItem
		if err := rows.Scan(&item.Username, &item.Nickname, &item.AvatarURL); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "scan following profiles failed"})
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "read following profiles failed"})
		return
	}

	writeJSON(w, http.StatusOK, apiResponse{Code: 0, Data: items})
}

func PublicProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Code: 1, Msg: "method not allowed"})
		return
	}

	userID := strings.TrimSpace(r.URL.Query().Get("user_id"))
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "missing user_id"})
		return
	}

	db := mysql_client.Get()
	if db == nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "mysql client not initialized"})
		return
	}

	profile, _, err := loadAccountProfileWithPassword(r.Context(), db, userID)
	if err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, apiResponse{Code: 1, Msg: "user not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "load public profile failed"})
		return
	}

	var followingCount int64
	if err := db.QueryRowContext(r.Context(), "SELECT COUNT(1) FROM follows WHERE user_id = ?", userID).Scan(&followingCount); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "count following failed"})
		return
	}

	var followerCount int64
	if err := db.QueryRowContext(r.Context(), "SELECT COUNT(1) FROM follows WHERE target_user_id = ?", userID).Scan(&followerCount); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "count followers failed"})
		return
	}

	var likeCount int64
	if err := db.QueryRowContext(r.Context(), "SELECT COUNT(1) FROM video_likes WHERE author_id = ?", userID).Scan(&likeCount); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "count likes failed"})
		return
	}

	videos, err := loadAccountVideoCards(r.Context(), userID, 60)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "load public videos failed"})
		return
	}

	writeJSON(w, http.StatusOK, apiResponse{
		Code: 0,
		Data: publicAccountProfile{
			Username:       profile.Username,
			Nickname:       profile.Nickname,
			AvatarURL:      profile.AvatarURL,
			Age:            profile.Age,
			Address:        profile.Address,
			Signature:      profile.Signature,
			FollowingCount: followingCount,
			FollowerCount:  followerCount,
			LikeCount:      likeCount,
			Videos:         videos,
		},
	})
}

func UpdateProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Code: 1, Msg: "method not allowed"})
		return
	}

	username, err := requireCurrentUsername(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, apiResponse{Code: 1, Msg: "login required"})
		return
	}

	var req accountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "invalid json"})
		return
	}
	req.Nickname = strings.TrimSpace(req.Nickname)
	req.AvatarURL = strings.TrimSpace(req.AvatarURL)
	req.Address = strings.TrimSpace(req.Address)
	req.Signature = strings.TrimSpace(req.Signature)
	req.SecurityQuestion = strings.TrimSpace(req.SecurityQuestion)
	req.SecurityAnswer = strings.TrimSpace(req.SecurityAnswer)
	if req.Nickname == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "nickname is required"})
		return
	}
	if req.SecurityQuestion == "" {
		req.SecurityQuestion = "默认问题：请输入默认答案 123456"
	}

	db := mysql_client.Get()
	if db == nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "mysql client not initialized"})
		return
	}

	if req.SecurityAnswer == "" {
		if _, err := db.ExecContext(r.Context(),
			`UPDATE users
			 SET nickname = ?, avatar_url = ?, age = ?, address = ?, signature = ?, security_question = ?
			 WHERE username = ?`,
			req.Nickname, req.AvatarURL, nullableAgeValue(req.Age), req.Address, req.Signature, req.SecurityQuestion, username,
		); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "update profile failed"})
			return
		}
	} else if _, err := db.ExecContext(r.Context(),
		`UPDATE users
		 SET nickname = ?, avatar_url = ?, age = ?, address = ?, signature = ?, security_question = ?, security_answer_hash = ?
		 WHERE username = ?`,
		req.Nickname, req.AvatarURL, nullableAgeValue(req.Age), req.Address, req.Signature, req.SecurityQuestion, hashPassword(req.SecurityAnswer), username,
	); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "update profile failed"})
		return
	}

	profile, _, err := loadAccountProfileWithPassword(r.Context(), db, username)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "load profile failed"})
		return
	}

	writeJSON(w, http.StatusOK, apiResponse{Code: 0, Msg: "profile updated", Data: profile})
}

func ChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Code: 1, Msg: "method not allowed"})
		return
	}

	username, err := requireCurrentUsername(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, apiResponse{Code: 1, Msg: "login required"})
		return
	}

	var req changePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "invalid json"})
		return
	}
	req.OldPassword = strings.TrimSpace(req.OldPassword)
	req.NewPassword = strings.TrimSpace(req.NewPassword)
	if len(req.OldPassword) < 6 || len(req.NewPassword) < 6 {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "password too short"})
		return
	}

	db := mysql_client.Get()
	if db == nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "mysql client not initialized"})
		return
	}

	_, passwordHash, err := loadAccountProfileWithPassword(r.Context(), db, username)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "load account failed"})
		return
	}
	if passwordHash != hashPassword(req.OldPassword) {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "old password incorrect"})
		return
	}

	if _, err := db.ExecContext(r.Context(),
		`UPDATE users SET password_hash = ? WHERE username = ?`,
		hashPassword(req.NewPassword), username,
	); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "change password failed"})
		return
	}

	writeJSON(w, http.StatusOK, apiResponse{Code: 0, Msg: "password changed"})
}

func SecurityQuestion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Code: 1, Msg: "method not allowed"})
		return
	}

	username := strings.TrimSpace(r.URL.Query().Get("username"))
	if username == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "missing username"})
		return
	}

	db := mysql_client.Get()
	if db == nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "mysql client not initialized"})
		return
	}

	var question string
	if err := db.QueryRowContext(r.Context(),
		`SELECT security_question FROM users WHERE username = ? LIMIT 1`,
		username,
	).Scan(&question); err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "user not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "load security question failed"})
		return
	}

	writeJSON(w, http.StatusOK, apiResponse{
		Code: 0,
		Data: map[string]string{"security_question": question},
	})
}

func ResetPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Code: 1, Msg: "method not allowed"})
		return
	}

	var req resetPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "invalid json"})
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	req.SecurityAnswer = strings.TrimSpace(req.SecurityAnswer)
	if req.Username == "" || req.SecurityAnswer == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "missing username or security_answer"})
		return
	}

	db := mysql_client.Get()
	if db == nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "mysql client not initialized"})
		return
	}

	var answerHash string
	if err := db.QueryRowContext(r.Context(),
		`SELECT security_answer_hash FROM users WHERE username = ? LIMIT 1`,
		req.Username,
	).Scan(&answerHash); err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "user not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "load security answer failed"})
		return
	}
	if answerHash != hashPassword(req.SecurityAnswer) {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "security answer incorrect"})
		return
	}

	if _, err := db.ExecContext(r.Context(),
		`UPDATE users SET password_hash = ? WHERE username = ?`,
		hashPassword("123456"), req.Username,
	); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "reset password failed"})
		return
	}

	writeJSON(w, http.StatusOK, apiResponse{Code: 0, Msg: "password reset to 123456"})
}

func currentUsername(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return "", false
	}

	sessionStore.RLock()
	username, ok := sessionStore.byToken[cookie.Value]
	sessionStore.RUnlock()
	if !ok {
		return "", false
	}
	return username, true
}

func hashPassword(password string) string {
	sum := sha256.Sum256([]byte(password))
	return hex.EncodeToString(sum[:])
}

func newSessionToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   7 * 24 * 60 * 60,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func requireCurrentUsername(r *http.Request) (string, error) {
	username, ok := currentUsername(r)
	if !ok {
		return "", fmt.Errorf("not logged in")
	}
	return username, nil
}

func loadAccountProfileWithPassword(ctx context.Context, db *sql.DB, username string) (accountProfile, string, error) {
	var (
		profile      accountProfile
		passwordHash string
		age          sql.NullInt64
		avatarURL    sql.NullString
		address      sql.NullString
		signature    sql.NullString
		question     sql.NullString
	)

	if err := db.QueryRowContext(ctx,
		`SELECT username, nickname, avatar_url, age, address, signature, security_question, password_hash
		 FROM users
		 WHERE username = ?
		 LIMIT 1`,
		username,
	).Scan(
		&profile.Username,
		&profile.Nickname,
		&avatarURL,
		&age,
		&address,
		&signature,
		&question,
		&passwordHash,
	); err != nil {
		return accountProfile{}, "", err
	}

	if age.Valid {
		v := int(age.Int64)
		profile.Age = &v
	}
	if avatarURL.Valid {
		profile.AvatarURL = avatarURL.String
	}
	if address.Valid {
		profile.Address = address.String
	}
	if signature.Valid {
		profile.Signature = signature.String
	}
	if question.Valid {
		profile.SecurityQuestion = question.String
	}
	return profile, passwordHash, nil
}

func nullableAgeValue(age *int) interface{} {
	if age == nil {
		return nil
	}
	return *age
}
