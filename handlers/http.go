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

func UI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(uiHTML))
}

const uiHTML = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>视频 Feed 流系统</title>
  <style>
    body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "PingFang SC", "Noto Sans CJK SC", "Microsoft YaHei", Arial, sans-serif; margin: 24px; }
    .row { display: flex; gap: 12px; flex-wrap: wrap; align-items: end; }
    .card { border: 1px solid #e5e7eb; border-radius: 10px; padding: 16px; margin: 12px 0; }
    label { display: block; font-size: 12px; color: #374151; margin-bottom: 6px; }
    input { padding: 8px 10px; border: 1px solid #d1d5db; border-radius: 8px; min-width: 220px; }
    button { padding: 9px 12px; border-radius: 10px; border: 1px solid #111827; background: #111827; color: white; cursor: pointer; }
    button.secondary { background: white; color: #111827; border: 1px solid #d1d5db; }
    button:disabled { opacity: .5; cursor: not-allowed; }
    pre { background: #0b1020; color: #e5e7eb; padding: 12px; border-radius: 10px; overflow: auto; }
    .hint { color: #6b7280; font-size: 12px; }
  </style>
</head>
<body>
  <h2>视频 Feed 流系统（MVP）</h2>
  <div class="card">
    <div class="row">
      <div>
        <label>当前用户 user_id</label>
        <input id="userId" placeholder="例如 1001" />
      </div>
      <div>
        <label>目标用户 target_user_id（关注用）</label>
        <input id="targetUserId" placeholder="例如 2001" />
      </div>
      <div>
        <label>video_id（发布用）</label>
        <input id="videoId" placeholder="例如 vid_001" />
      </div>
    </div>
    <p class="hint">提示：先填 user_id，再发布 / 关注 / 拉取。分页会自动记住 next_cursor。</p>
  </div>

  <div class="card">
    <h3>动作</h3>
    <div class="row">
      <button id="btnPublish">发布视频</button>
      <button id="btnFollow" class="secondary">关注</button>
      <button id="btnUnfollow" class="secondary">取关</button>
      <button id="btnFollowing" class="secondary">查看关注列表</button>
    </div>
  </div>

  <div class="card">
    <h3>拉取</h3>
    <div class="row">
      <button id="btnMyFeed">我的时间线（/feed）</button>
      <button id="btnMyFeedPrev" class="secondary" disabled>上一页（我的时间线）</button>
      <button id="btnMyFeedNext" class="secondary" disabled>下一页（我的时间线）</button>
      <button id="btnHomeFeed">关注页（/home_feed）</button>
      <button id="btnHomeFeedPrev" class="secondary" disabled>上一页（关注页）</button>
      <button id="btnHomeFeedNext" class="secondary" disabled>下一页（关注页）</button>
    </div>
  </div>

  <div class="card">
    <h3>输出</h3>
    <pre id="out">{}</pre>
  </div>

  <script>
    const out = document.getElementById("out");
    const userIdEl = document.getElementById("userId");
    const targetUserIdEl = document.getElementById("targetUserId");
    const videoIdEl = document.getElementById("videoId");

    let myPageCursor = null;
    let myNextCursor = null;
    let myHistory = [];

    let homePageCursor = null;
    let homeNextCursor = null;
    let homeHistory = [];

    function userId() { return (userIdEl.value || "").trim(); }
    function targetUserId() { return (targetUserIdEl.value || "").trim(); }
    function videoId() { return (videoIdEl.value || "").trim(); }

    function formatTimestamp(ms) {
      if (typeof ms !== "number") return ms;
      const d = new Date(ms);
      if (Number.isNaN(d.getTime())) return ms;
      const pad = (n) => String(n).padStart(2, "0");
      return d.getFullYear() + "-" +
        pad(d.getMonth() + 1) + "-" +
        pad(d.getDate()) + " " +
        pad(d.getHours()) + ":" +
        pad(d.getMinutes()) + ":" +
        pad(d.getSeconds());
    }

    function decorate(obj) {
      if (Array.isArray(obj)) {
        return obj.map(decorate);
      }
      if (obj && typeof obj === "object") {
        const copy = {};
        for (const [k, v] of Object.entries(obj)) {
          copy[k] = decorate(v);
          if (k === "publish_time" && typeof v === "number") {
            copy.publish_time_text = formatTimestamp(v);
          }
          if (k === "score" && typeof v === "number") {
            copy.score_text = formatTimestamp(v);
          }
        }
        return copy;
      }
      return obj;
    }

    function setOutput(obj) {
      out.textContent = JSON.stringify(decorate(obj), null, 2);
    }

    async function getJSON(url) {
      const resp = await fetch(url, { method: "GET" });
      const data = await resp.json().catch(() => ({ code: 999, msg: "invalid json response" }));
      return data;
    }

    async function postJSON(url, body) {
      const resp = await fetch(url, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      const data = await resp.json().catch(() => ({ code: 999, msg: "invalid json response" }));
      return data;
    }

    function updateButtons() {
      document.getElementById("btnMyFeedPrev").disabled = myHistory.length === 0;
      document.getElementById("btnMyFeedNext").disabled = !myNextCursor;
      document.getElementById("btnHomeFeedPrev").disabled = homeHistory.length === 0;
      document.getElementById("btnHomeFeedNext").disabled = !homeNextCursor;
    }

    async function publish() {
      const u = userId();
      const v = videoId();
      const data = await postJSON("/publish", { user_id: u, video_id: v });
      setOutput(data);
    }

    async function follow() {
      const u = userId();
      const t = targetUserId();
      const data = await postJSON("/follow", { user_id: u, target_user_id: t });
      setOutput(data);
    }

    async function unfollow() {
      const u = userId();
      const t = targetUserId();
      const data = await postJSON("/unfollow", { user_id: u, target_user_id: t });
      setOutput(data);
    }

    async function following() {
      const u = userId();
      const data = await getJSON("/following?user_id=" + encodeURIComponent(u));
      setOutput(data);
    }

    async function myFeed(pageCursor) {
      const u = userId();
      let url = "/feed?user_id=" + encodeURIComponent(u) + "&limit=20";
      if (pageCursor) {
        url += "&cursor_score=" + encodeURIComponent(pageCursor.score) + "&cursor_video_id=" + encodeURIComponent(pageCursor.video_id);
      }
      const data = await getJSON(url);
      myPageCursor = pageCursor;
      myNextCursor = data.next_cursor || null;
      updateButtons();
      setOutput(data);
    }

    async function homeFeed(pageCursor) {
      const u = userId();
      let url = "/home_feed?user_id=" + encodeURIComponent(u) + "&limit=20";
      if (pageCursor) {
        url += "&cursor_score=" + encodeURIComponent(pageCursor.score) + "&cursor_video_id=" + encodeURIComponent(pageCursor.video_id);
      }
      const data = await getJSON(url);
      homePageCursor = pageCursor;
      homeNextCursor = data.next_cursor || null;
      updateButtons();
      setOutput(data);
    }

    async function myFeedFirst() {
      myHistory = [];
      myPageCursor = null;
      myNextCursor = null;
      updateButtons();
      await myFeed(null);
    }

    async function myFeedNext() {
      if (!myNextCursor) return;
      myHistory.push(myPageCursor);
      await myFeed(myNextCursor);
    }

    async function myFeedPrev() {
      if (myHistory.length === 0) return;
      const prevCursor = myHistory.pop();
      await myFeed(prevCursor);
    }

    async function homeFeedFirst() {
      homeHistory = [];
      homePageCursor = null;
      homeNextCursor = null;
      updateButtons();
      await homeFeed(null);
    }

    async function homeFeedNext() {
      if (!homeNextCursor) return;
      homeHistory.push(homePageCursor);
      await homeFeed(homeNextCursor);
    }

    async function homeFeedPrev() {
      if (homeHistory.length === 0) return;
      const prevCursor = homeHistory.pop();
      await homeFeed(prevCursor);
    }

    document.getElementById("btnPublish").addEventListener("click", () => publish());
    document.getElementById("btnFollow").addEventListener("click", () => follow());
    document.getElementById("btnUnfollow").addEventListener("click", () => unfollow());
    document.getElementById("btnFollowing").addEventListener("click", () => following());

    document.getElementById("btnMyFeed").addEventListener("click", () => myFeedFirst());
    document.getElementById("btnMyFeedPrev").addEventListener("click", () => myFeedPrev());
    document.getElementById("btnMyFeedNext").addEventListener("click", () => myFeedNext());
    document.getElementById("btnHomeFeed").addEventListener("click", () => homeFeedFirst());
    document.getElementById("btnHomeFeedPrev").addEventListener("click", () => homeFeedPrev());
    document.getElementById("btnHomeFeedNext").addEventListener("click", () => homeFeedNext());

    updateButtons();
    setOutput({ code: 0, msg: "ready" });
  </script>
</body>
</html>`
