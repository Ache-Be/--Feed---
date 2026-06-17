package handlers

import (
	"encoding/json"
	"net/http"
)

type apiResponse struct {
	Code       int          `json:"code"`
	Msg        string       `json:"msg,omitempty"`
	Data       interface{}  `json:"data,omitempty"`
	NextCursor *cursorToken `json:"next_cursor,omitempty"`
}

// cursorToken 是“游标分页”的游标信息。
//
// 为什么要用双字段（score + video_id）？
// - 仅用 score（时间戳）做游标时，如果同一毫秒内写入多条数据，它们的 score 相同
// - 此时如果用“开区间”< lastScore 翻页，会把同 score 的剩余数据整体跳过，导致漏数据
// - 因此需要把“时间戳 + member”组合起来，精确定位到“翻到哪一条”，做到不重不漏
type cursorToken struct {
	Score   int64  `json:"score"`
	VideoID string `json:"video_id"`
}

func writeJSON(w http.ResponseWriter, status int, resp apiResponse) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}
