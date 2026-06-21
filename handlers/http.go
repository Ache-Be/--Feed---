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
    body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "PingFang SC", "Noto Sans CJK SC", "Microsoft YaHei", Arial, sans-serif; margin: 0; background: #000; color: #111827; overflow: hidden; }
    .app { max-width: none; padding: 0; margin: 0; height: 100vh; overflow: hidden; position: relative; background: #000; }
    .topbar { position: fixed; top: 0; left: 0; right: 0; z-index: 20; padding: 12px 16px; background: rgba(0, 0, 0, 0.5); backdrop-filter: blur(4px); display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
    .brand { font-size: 22px; font-weight: 700; margin-right: auto; color: #fff; }
    .search { display: flex; gap: 8px; flex: 1 1 420px; }
    .search input { flex: 1; background: rgba(255,255,255,0.12); color: #fff; border-color: rgba(255,255,255,0.18); }
    .search input::placeholder { color: rgba(255,255,255,0.7); }
    .status { color: rgba(255,255,255,0.86); font-size: 13px; }
    .tabs { position: fixed; top: 64px; left: 0; right: 0; z-index: 20; padding: 8px 16px; background: rgba(0, 0, 0, 0.3); backdrop-filter: blur(2px); display: flex; gap: 4px; flex-wrap: wrap; }
    .tab-btn { padding: 10px 16px; border-radius: 999px; border: 1px solid rgba(255,255,255,0.2); background: rgba(255,255,255,0.1); color: #fff; cursor: pointer; }
    .tab-btn.active { background: #fff; color: #000; border-color: #fff; }
    .tab-btn:disabled { opacity: .45; cursor: not-allowed; }
    .panel { display: none; }
    .panel.active { display: block; }
    .row { display: flex; gap: 12px; flex-wrap: wrap; align-items: end; }
    .card { background: white; border: 1px solid #e5e7eb; border-radius: 14px; padding: 16px; margin-bottom: 16px; box-shadow: 0 10px 30px rgba(15, 23, 42, 0.04); }
    .card h3 { margin: 0 0 12px 0; }
    .card h4 { margin: 0 0 12px 0; }
    label { display: block; font-size: 12px; color: #374151; margin-bottom: 6px; }
    input, textarea { width: 100%; box-sizing: border-box; padding: 10px 12px; border: 1px solid #d1d5db; border-radius: 10px; background: #fff; }
    textarea { min-height: 100px; resize: vertical; }
    button { padding: 10px 14px; border-radius: 10px; border: 1px solid #111827; background: #111827; color: white; cursor: pointer; }
    button.secondary { background: white; color: #111827; border: 1px solid #d1d5db; }
    button.danger { background: #b91c1c; border-color: #b91c1c; }
    .hint { color: #6b7280; font-size: 12px; margin-top: 6px; }
    .empty { color: #9ca3af; font-size: 14px; }
    .video-grid { display: block; padding: 0; margin: 0; height: 100vh; overflow-y: auto; overscroll-behavior-y: contain; scroll-snap-type: y mandatory; -webkit-overflow-scrolling: touch; }
    .video-item { position: relative; width: 100vw; height: 100vh; margin: 0; border: 0; border-radius: 0; padding: 0; background: #000; overflow: hidden; box-shadow: none; scroll-snap-align: start; scroll-snap-stop: always; }
    .video-stage { position: relative; width: 100%; height: 100%; background: #000; }
    .video-player { width: 100%; height: 100%; object-fit: contain; background: #000; display: block; }
    .video-cover { width: 100%; height: 100%; object-fit: cover; display: block; }
    .video-action-rail { position: absolute; right: 15px; top: 50%; transform: translateY(-50%); display: flex; flex-direction: column; gap: 12px; z-index: 10; align-items: center; }
    .video-overlay { position: absolute; inset: 0; pointer-events: none; }
    .video-bottom-bar { position: absolute; left: 0; bottom: 0; width: 100%; z-index: 10; background: linear-gradient(to top, rgba(0,0,0,0.85), transparent); padding: 15px 16px 20px 16px; display: flex; flex-direction: column; gap: 12px; box-sizing: border-box; pointer-events: none; }
    .video-info-row { display: flex; justify-content: space-between; align-items: center; gap: 12px; min-height: 54px; }
    .video-info-left { display: flex; align-items: center; gap: 12px; min-width: 0; max-width: calc(100% - 72px); pointer-events: auto; }
    .video-info-text { min-width: 0; }
    .video-author { margin: 0 0 4px 0; font-size: 17px; line-height: 1.2; font-weight: 700; color: #fff; }
    .video-desc-inline { font-size: 14px; color: rgba(255,255,255,0.92); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
    .video-info-spacer { width: 72px; flex-shrink: 0; }
    .progress-row-only { height: 20px; display: flex; align-items: center; pointer-events: auto; }
    .video-progress-top { width: 100%; accent-color: #f8fafc; margin: 0; }
    .video-controls-row { display: flex; justify-content: space-between; align-items: center; pointer-events: auto; }
    .video-controls-left, .video-controls-right { display: flex; align-items: center; gap: 12px; }
    .video-icon-btn { min-width: 36px; min-height: 36px; border-radius: 999px; border: 1px solid rgba(255,255,255,0.24); background: rgba(255,255,255,0.12); color: #fff; padding: 0 10px; display: inline-flex; align-items: center; justify-content: center; }
    .video-icon-btn.secondary { background: rgba(255,255,255,0.12); color: #fff; border-color: rgba(255,255,255,0.24); }
    .time-label { color: #f3f4f6; font-size: 13px; min-width: 98px; text-align: left; }
    .volume-slider { width: 88px; accent-color: #f8fafc; }
    .more-wrap { position: relative; }
    .more-menu { position: absolute; right: 0; bottom: 44px; display: none; min-width: 132px; background: rgba(15,23,42,0.96); border: 1px solid rgba(255,255,255,0.08); border-radius: 14px; box-shadow: 0 16px 36px rgba(0,0,0,0.28); padding: 6px; }
    .more-menu.show { display: block; }
    .more-item { width: 100%; background: transparent; border: 0; color: #fff; text-align: left; padding: 10px 12px; border-radius: 10px; }
    .more-item:hover { background: rgba(255,255,255,0.08); }
    .meta { font-size: 13px; color: #d1d5db; margin: 4px 0; line-height: 1.6; }
    .media { width: 100%; border-radius: 12px; background: #111827; margin-top: 8px; }
    .action-rail { display: flex; flex-direction: column; gap: 10px; align-items: center; pointer-events: auto; margin-left: 10px; flex-shrink: 0; }
    .action-btn { width: 56px; height: 56px; border-radius: 999px; background: rgba(255,255,255,0.18); border: 1px solid rgba(255,255,255,0.28); color: white; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 2px; font-size: 12px; }
    .action-btn span:first-child { font-size: 20px; line-height: 1; }
    .avatar-wrap { position: relative; width: 64px; height: 64px; }
    .avatar { width: 64px; height: 64px; border-radius: 999px; object-fit: cover; border: 2px solid rgba(255,255,255,0.7); background: rgba(255,255,255,0.12); }
    .avatar-follow-btn { position: absolute; left: 50%; bottom: -8px; transform: translateX(-50%); min-width: 52px; height: 26px; border-radius: 999px; border: 1px solid #ef4444; background: #ef4444; color: white; font-size: 12px; line-height: 1; padding: 0 10px; white-space: nowrap; }
    .avatar-follow-btn.is-following { background: rgba(255,255,255,0.94); color: #111827; border-color: rgba(255,255,255,0.94); }
    .avatar-follow-btn.hidden { display: none; }
    .mono { font-family: Consolas, "Courier New", monospace; }
    .banner { position: fixed; top: 112px; left: 16px; right: 16px; z-index: 20; padding: 10px 12px; border-radius: 12px; background: rgba(79,70,229,0.18); color: #e0e7ff; backdrop-filter: blur(6px); }
    .hidden { display: none; }
    .auth-shell { max-width: 640px; }
    .auth-switch { display: flex; gap: 10px; flex-wrap: wrap; margin-top: 14px; }
    .text-btn { background: transparent; color: #2563eb; border: 0; padding: 0; }
    .profile-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; align-items: start; }
    .profile-column { display: flex; flex-direction: column; gap: 16px; min-width: 0; }
    .profile-subgrid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; align-items: start; }
    .readonly { padding: 10px 12px; border: 1px dashed #d1d5db; border-radius: 10px; background: #f9fafb; color: #374151; }
    .upload-row { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
    .upload-row input[type="file"] { padding: 8px; }
    .avatar-preview { width: 96px; height: 96px; border-radius: 20px; object-fit: cover; background: #e5e7eb; }
    .account-follow-list { display: flex; flex-direction: column; gap: 10px; margin-top: 14px; }
    .account-follow-item { display: flex; align-items: center; gap: 12px; padding: 10px 12px; border: 1px solid #e5e7eb; border-radius: 14px; background: #fff; }
    .account-follow-avatar { width: 44px; height: 44px; border-radius: 999px; object-fit: cover; background: #e5e7eb; flex-shrink: 0; cursor: pointer; }
    .account-follow-name { font-weight: 600; color: #111827; cursor: pointer; }
    .account-follow-sub { color: #6b7280; font-size: 12px; }
    .published-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 12px; margin-top: 12px; }
    .published-item { position: relative; width: 100%; border: 1px solid #e5e7eb; border-radius: 12px; overflow: hidden; background: white; box-shadow: 0 6px 16px rgba(15, 23, 42, 0.04); cursor: pointer; }
    .published-cover { width: 100%; aspect-ratio: 4 / 5; object-fit: cover; display: block; background: #111827; }
    .published-meta { display: flex; align-items: center; justify-content: center; gap: 8px; padding: 8px 6px; color: #374151; font-size: 12px; }
    .published-like { font-weight: 600; color: #111827; }
    .published-gear { position: absolute; top: 6px; right: 6px; width: 28px; height: 28px; border-radius: 999px; background: rgba(17,24,39,0.78); border: 1px solid rgba(255,255,255,0.18); color: #fff; padding: 0; display: inline-flex; align-items: center; justify-content: center; z-index: 2; }
    .edit-cover-preview { width: 120px; aspect-ratio: 4 / 5; border-radius: 14px; object-fit: cover; display: block; background: #e5e7eb; border: 1px solid #e5e7eb; }
    .clickable-profile { cursor: pointer; }
    .public-profile-page { position: fixed; inset: 0; z-index: 1001; background: linear-gradient(180deg, #f8fafc 0%, #ffffff 52%, #eef2ff 100%); color: #0f172a; overflow: auto; display: none; }
    .public-profile-page.show { display: block; }
    .public-profile-shell { max-width: 1120px; margin: 0 auto; padding: 28px 20px 40px; }
    .public-profile-topbar { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 18px; }
    .public-profile-back { background: #fff; border-color: #dbeafe; color: #1d4ed8; }
    .public-profile-hero { display: grid; grid-template-columns: 132px minmax(0, 1fr); gap: 24px; padding: 24px; border-radius: 28px; background: linear-gradient(135deg, #ffffff, #eff6ff 55%, #eef2ff); border: 1px solid #dbeafe; box-shadow: 0 26px 60px rgba(148,163,184,0.22); }
    .public-profile-avatar { width: 132px; height: 132px; border-radius: 999px; object-fit: cover; background: #e2e8f0; border: 3px solid #ffffff; box-shadow: 0 14px 30px rgba(59,130,246,0.14); }
    .public-profile-main { min-width: 0; display: flex; flex-direction: column; gap: 14px; }
    .public-profile-name { font-size: 34px; font-weight: 800; line-height: 1.1; }
    .public-profile-sub { color: #64748b; font-size: 14px; display: flex; gap: 12px; flex-wrap: wrap; }
    .public-profile-signature { color: #334155; line-height: 1.8; white-space: pre-wrap; }
    .public-profile-stats { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 12px; }
    .public-profile-stat { padding: 16px 18px; border-radius: 18px; background: #ffffff; border: 1px solid #e2e8f0; box-shadow: inset 0 1px 0 rgba(255,255,255,0.8); }
    .public-profile-stat strong { display: block; font-size: 26px; color: #0f172a; margin-bottom: 4px; }
    .public-profile-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 14px; margin-top: 22px; }
    .public-profile-work { border-radius: 18px; overflow: hidden; background: #fff; border: 1px solid #e2e8f0; box-shadow: 0 14px 28px rgba(148,163,184,0.16); cursor: pointer; }
    .public-profile-work-cover { width: 100%; aspect-ratio: 1 / 1; object-fit: cover; display: block; background: #cbd5e1; }
    .public-profile-work-meta { padding: 12px 14px; color: #475569; font-size: 13px; display: flex; justify-content: space-between; gap: 8px; }
    .edit-video-modal { width: min(640px, 100%); background: white; border-radius: 24px; padding: 20px; box-shadow: 0 24px 60px rgba(15,23,42,0.28); max-height: 84vh; overflow: auto; }
    .toast { position: fixed; left: 50%; top: 148px; max-width: min(420px, calc(100vw - 32px)); background: rgba(17,24,39,0.92); color: white; padding: 12px 16px; border-radius: 12px; box-shadow: 0 14px 34px rgba(15,23,42,0.24); opacity: 0; transform: translate(-50%, -12px); transition: all .2s ease; pointer-events: none; z-index: 999; text-align: center; }
    .toast.show { opacity: 1; transform: translate(-50%, 0); }
    .sheet-backdrop { position: fixed; inset: 0; background: rgba(15,23,42,0.58); display: none; align-items: flex-end; justify-content: center; padding: 24px; z-index: 998; }
    .sheet-backdrop.show { display: flex; }
    .detail-sheet { width: min(760px, 100%); max-height: 84vh; overflow: auto; background: white; border-radius: 24px; padding: 20px; box-shadow: 0 24px 60px rgba(15,23,42,0.28); }
    .detail-head { display: flex; justify-content: space-between; gap: 12px; align-items: center; margin-bottom: 14px; }
    .detail-head h3 { margin: 0; }
    .detail-media { width: 100%; border-radius: 18px; background: #111827; max-height: 56vh; object-fit: cover; }
    .detail-text { color: #374151; line-height: 1.8; white-space: pre-wrap; }
    .cropper-backdrop { position: fixed; inset: 0; background: rgba(15,23,42,0.72); display: none; align-items: center; justify-content: center; padding: 20px; z-index: 1000; }
    .cropper-backdrop.show { display: flex; }
    .cropper-modal { width: min(560px, 100%); background: white; border-radius: 22px; padding: 20px; box-shadow: 0 24px 64px rgba(15,23,42,0.32); }
    .cropper-box { width: min(320px, 78vw); height: min(320px, 78vw); margin: 16px auto; position: relative; overflow: hidden; border-radius: 18px; background: #111827; cursor: grab; touch-action: none; }
    .cropper-box.dragging { cursor: grabbing; }
    .cropper-image { position: absolute; user-select: none; -webkit-user-drag: none; }
    .cropper-mask { position: absolute; inset: 0; box-shadow: inset 0 0 0 2px rgba(255,255,255,0.75), inset 0 0 0 999px rgba(0,0,0,0.18); pointer-events: none; border-radius: 18px; }
    pre { background: #0b1020; color: #e5e7eb; padding: 12px; border-radius: 12px; overflow: auto; }
    #panelRecommend, #panelHot { width: 100vw; height: 100vh; }
    #panelRecommend .card:first-child, #panelHot .card:first-child { display: none; }
    #panelRecommend .card:last-child, #panelHot .card:last-child { margin: 0; padding: 0; background: transparent; border: 0; border-radius: 0; box-shadow: none; }
    #panelRecommend .card:last-child h3, #panelHot .card:last-child h3 { display: none; }
    #recommendCards, #hotCards { height: 100vh; }
    #panelPublish, #panelAccount { padding: 120px 16px 24px; height: 100vh; overflow: auto; background: #f5f7fb; box-sizing: border-box; }
    @media (max-width: 1100px) {
      .profile-subgrid { grid-template-columns: 1fr; }
    }
    @media (max-width: 900px) {
      .profile-grid { grid-template-columns: 1fr; }
      .published-grid { grid-template-columns: repeat(3, minmax(0, 1fr)); }
    }
    @media (max-width: 640px) {
      .published-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
    }
  </style>
</head>
<body>
  <div class="app">
    <div class="topbar">
      <div class="brand">视频 Feed 流系统</div>
      <div class="search">
        <input id="searchInput" placeholder="搜索作者 / 简介" />
        <button id="btnSearch" class="secondary">搜索</button>
      </div>
      <div id="userStatus" class="status">未登录</div>
    </div>

    <div id="loginBanner" class="banner">当前未登录。请先到“账号”页登录后再发布内容。</div>

    <div class="tabs">
      <button id="tabRecommend" class="tab-btn active">推荐</button>
      <button id="tabHot" class="tab-btn">热榜</button>
      <button id="tabPublish" class="tab-btn" disabled>发布</button>
      <button id="tabAccount" class="tab-btn">账号</button>
    </div>

    <section id="panelRecommend" class="panel active">
      <div class="card">
        <div class="row">
          <button id="btnRecommendRefresh">刷新推荐</button>
          <button id="btnSearchRecommend" class="secondary">按当前搜索推荐</button>
        </div>
        <div class="hint">推荐流当前按最新发布展示，支持按作者或简介搜索。</div>
      </div>
      <div class="card">
        <h3>推荐视频</h3>
        <div id="recommendCards" class="video-grid">
          <div class="empty">正在等待加载推荐内容。</div>
        </div>
      </div>
    </section>

    <section id="panelHot" class="panel">
      <div class="card">
        <div class="row">
          <button id="btnHotRefresh">刷新热榜</button>
          <button id="btnSearchHot" class="secondary">按当前搜索热榜</button>
        </div>
        <div class="hint">热榜当前按作者粉丝数优先、发布时间次之排序，先用于演示整体效果。</div>
      </div>
      <div class="card">
        <h3>热榜视频</h3>
        <div id="hotCards" class="video-grid">
          <div class="empty">正在等待加载热榜内容。</div>
        </div>
      </div>
    </section>

    <section id="panelPublish" class="panel">
      <div class="card">
        <h3>发布视频</h3>
        <div class="row">
          <div style="flex: 1 1 220px;">
            <label>video_id（可不填）</label>
            <input id="videoId" placeholder="留空自动生成" />
          </div>
          <div style="flex: 1 1 280px;">
            <label>本地封面</label>
            <input id="coverUrl" type="hidden" />
            <div id="coverUploadStatus" class="readonly">尚未上传封面，可选。</div>
            <div class="upload-row" style="margin-top: 8px;">
              <input id="coverFile" type="file" accept=".jpg,.jpeg,.png,.webp,.gif" />
              <button id="btnUploadCover" class="secondary" type="button">上传本地封面</button>
            </div>
          </div>
          <div style="flex: 1 1 280px;">
            <label>本地视频</label>
            <input id="videoUrl" type="hidden" />
            <div id="videoUploadStatus" class="readonly">请先上传本地视频文件。</div>
            <div class="upload-row" style="margin-top: 8px;">
              <input id="videoFile" type="file" accept=".mp4,.webm,.ogg" />
              <button id="btnUploadVideo" class="secondary" type="button">上传本地视频</button>
            </div>
          </div>
          <div style="flex: 1 1 100%;">
            <label>描述 description</label>
            <textarea id="description" placeholder="简单描述一下这条内容"></textarea>
          </div>
        </div>
        <div class="hint">当前作者自动使用登录用户，未登录时无法发布。</div>
        <div class="auth-switch">
          <button id="btnPublish">发布视频</button>
        </div>
      </div>
      <div class="card">
        <h3>刚刚发布</h3>
        <div id="publishCards" class="video-grid">
          <div class="empty">发布成功后，这里会显示你刚刚提交的视频卡片。</div>
        </div>
      </div>
    </section>

    <section id="panelAccount" class="panel">
      <div id="accountGuest" class="auth-shell">
        <div id="guestLogin" class="card">
          <h3>登录</h3>
          <label>账号</label>
          <input id="loginUsername" placeholder="输入账号" />
          <label style="margin-top: 10px;">密码</label>
          <input id="loginPassword" type="password" placeholder="输入密码" />
          <div class="auth-switch">
            <button id="btnLogin">登录</button>
            <button id="btnToRegister" class="text-btn" type="button">还没有账号？去注册</button>
            <button id="btnToRecover" class="text-btn" type="button">忘记密码？</button>
          </div>
        </div>

        <div id="guestRegister" class="card hidden">
          <h3>注册</h3>
          <div class="row">
            <div style="flex: 1 1 220px;">
              <label>账号</label>
              <input id="registerUsername" placeholder="至少 3 位" />
            </div>
            <div style="flex: 1 1 220px;">
              <label>密码</label>
              <input id="registerPassword" type="password" placeholder="至少 6 位" />
            </div>
            <div style="flex: 1 1 220px;">
              <label>昵称（必填）</label>
              <input id="registerNickname" placeholder="展示给其他人的昵称" />
            </div>
            <div style="flex: 1 1 120px;">
              <label>年龄（选填）</label>
              <input id="registerAge" placeholder="例如 22" />
            </div>
            <div style="flex: 1 1 220px;">
              <label>地址（选填）</label>
              <input id="registerAddress" placeholder="例如 广东深圳" />
            </div>
            <div style="flex: 1 1 100%;">
              <label>签名（选填）</label>
              <input id="registerSignature" placeholder="介绍一下自己" />
            </div>
            <div style="flex: 1 1 100%;">
              <label>密保问题</label>
              <input id="registerSecurityQuestion" value="默认问题：请输入默认答案 123456" />
            </div>
            <div style="flex: 1 1 220px;">
              <label>密保答案</label>
              <input id="registerSecurityAnswer" value="123456" />
            </div>
          </div>
          <div class="auth-switch">
            <button id="btnRegister">注册</button>
            <button id="btnBackToLogin1" class="secondary" type="button">返回登录</button>
          </div>
        </div>

        <div id="guestRecover" class="card hidden">
          <h3>忘记密码</h3>
          <label>账号</label>
          <input id="resetUsername" placeholder="先输入账号再读取密保问题" />
          <div class="auth-switch">
            <button id="btnLoadQuestion" class="secondary">读取密保问题</button>
          </div>
          <label style="margin-top: 10px;">密保问题</label>
          <div id="resetQuestion" class="readonly">请先输入账号并读取密保问题</div>
          <label style="margin-top: 10px;">密保答案</label>
          <input id="resetAnswer" value="123456" placeholder="回答正确后会重置为 123456" />
          <div class="auth-switch">
            <button id="btnResetPassword">验证并重置为 123456</button>
            <button id="btnBackToLogin2" class="secondary" type="button">返回登录</button>
          </div>
          <div class="hint">这块先做最简版，用来补账号闭环，不追求复杂安全策略。</div>
        </div>
      </div>

      <div id="accountAuthed" class="hidden">
        <div class="profile-grid">
          <div class="profile-column">
            <div class="card">
              <h3>账号资料</h3>
              <label>账号</label>
              <div id="profileUsername" class="readonly mono"></div>
              <label style="margin-top: 10px;">头像</label>
              <img id="profileAvatarPreview" class="avatar-preview" alt="avatar preview" />
              <input id="profileAvatarUrl" type="hidden" />
              <div class="upload-row" style="margin-top: 8px;">
                <input id="avatarFile" type="file" accept=".jpg,.jpeg,.png,.webp,.gif" />
                <button id="btnUploadAvatar" class="secondary" type="button">上传头像</button>
              </div>
              <label style="margin-top: 10px;">昵称（必填）</label>
              <input id="profileNickname" placeholder="昵称" />
              <label style="margin-top: 10px;">年龄（选填）</label>
              <input id="profileAge" placeholder="例如 22" />
              <label style="margin-top: 10px;">地址（选填）</label>
              <input id="profileAddress" placeholder="例如 广东深圳" />
              <label style="margin-top: 10px;">签名（选填）</label>
              <textarea id="profileSignature" placeholder="介绍一下自己"></textarea>
              <label style="margin-top: 10px;">密保问题</label>
              <input id="profileSecurityQuestion" placeholder="例如 你的默认答案是什么？" />
              <label style="margin-top: 10px;">密保答案</label>
              <input id="profileSecurityAnswer" placeholder="留空表示不修改当前密保答案" />
              <div class="auth-switch">
                <button id="btnSaveProfile">保存资料</button>
              </div>
            </div>

            <div class="profile-subgrid">
              <div class="card">
                <h3>当前账号</h3>
                <div class="meta">已登录用户：<span id="currentUsername" class="mono"></span></div>
                <div class="auth-switch">
                  <button id="btnLogout" class="danger">退出登录</button>
                </div>
              </div>

              <div class="card">
                <div class="row" style="justify-content: space-between; align-items: center;">
                  <h3 style="margin:0;">我的关注</h3>
                  <button id="btnRefreshFollowingProfiles" class="secondary" type="button">刷新</button>
                </div>
                <div class="meta" style="color:#374151;">当前已关注 <span id="followingCount">0</span> 个用户</div>
                <div id="followingList" class="account-follow-list">
                  <div class="empty">登录后，这里会显示你关注的账号昵称和头像。</div>
                </div>
              </div>
            </div>
          </div>

          <div class="profile-column">
            <div class="card">
              <h3>改密码</h3>
              <label>旧密码</label>
              <input id="oldPassword" type="password" placeholder="输入当前密码" />
              <label style="margin-top: 10px;">新密码</label>
              <input id="newPassword" type="password" placeholder="至少 6 位" />
              <div class="auth-switch">
                <button id="btnChangePassword">确认改密码</button>
              </div>
              <div class="hint">如果忘记密码，可以先退出登录，再走“忘记密码”流程重置为 123456。</div>
            </div>

            <div class="card">
              <h3>我的发布</h3>
              <div class="hint">当前按一行 3 个展示；点击封面可跳到视频流，右上角齿轮可编辑作品。</div>
              <div id="accountMyVideos" class="published-grid">
                <div class="empty">登录后，这里会显示你发布过的视频。</div>
              </div>
            </div>
          </div>
        </div>

      </div>
    </section>

    <div class="card">
      <h3>调试输出</h3>
      <pre id="out">{}</pre>
    </div>
  </div>
  <section id="publicProfilePage" class="public-profile-page">
    <div class="public-profile-shell">
      <div class="public-profile-topbar">
        <button id="btnBackFromProfile" class="public-profile-back secondary" type="button">返回</button>
        <div id="publicProfileRoute" class="meta" style="color:#64748b;"></div>
      </div>
      <div class="public-profile-hero">
        <img id="publicProfileAvatar" class="public-profile-avatar" alt="profile avatar" />
        <div class="public-profile-main">
          <div id="publicProfileName" class="public-profile-name">创作者</div>
          <div id="publicProfileSub" class="public-profile-sub"></div>
          <div id="publicProfileSignature" class="public-profile-signature">正在加载个人简介...</div>
          <div class="public-profile-stats">
            <div class="public-profile-stat"><strong id="publicProfileFollowing">0</strong><span>关注</span></div>
            <div class="public-profile-stat"><strong id="publicProfileFollowers">0</strong><span>粉丝</span></div>
            <div class="public-profile-stat"><strong id="publicProfileLikes">0</strong><span>获赞</span></div>
          </div>
        </div>
      </div>
      <div style="margin-top:24px;">
        <h3 style="margin:0 0 12px 0; font-size:24px;">Ta 的作品</h3>
        <div id="publicProfileWorks" class="public-profile-grid">
          <div class="empty">正在加载作品...</div>
        </div>
      </div>
    </div>
  </section>
  <div id="toast" class="toast"></div>
  <div id="detailSheetBackdrop" class="sheet-backdrop">
    <div class="detail-sheet">
      <div class="detail-head">
        <h3 id="detailSheetAuthor">@作者</h3>
        <button id="btnCloseDetail" class="secondary" type="button">关闭</button>
      </div>
      <div id="detailSheetTime" class="meta" style="color:#6b7280;"></div>
      <video id="detailSheetVideo" class="detail-media hidden" controls playsinline preload="metadata"></video>
      <img id="detailSheetCover" class="detail-media hidden" alt="detail cover" />
      <div id="detailSheetDesc" class="detail-text" style="margin-top:14px;"></div>
    </div>
  </div>
  <div id="editVideoBackdrop" class="sheet-backdrop">
    <div class="edit-video-modal">
      <div class="detail-head">
        <h3>编辑作品</h3>
        <button id="btnCloseEditVideo" class="secondary" type="button">关闭</button>
      </div>
      <div id="editVideoMeta" class="meta" style="color:#6b7280;"></div>
      <label>封面预览</label>
      <img id="editVideoCoverPreview" class="edit-cover-preview" alt="edit cover preview" />
      <label style="margin-top:12px;">重新上传封面</label>
      <input id="editVideoCoverUrl" type="hidden" />
      <div id="editVideoCoverStatus" class="readonly">当前未更换封面，直接保存会保留原封面。</div>
      <div class="upload-row" style="margin-top: 8px;">
        <input id="editCoverFile" type="file" accept=".jpg,.jpeg,.png,.webp,.gif" />
        <button id="btnUploadEditCover" class="secondary" type="button">上传新封面</button>
      </div>
      <label>简介</label>
      <textarea id="editVideoDescription" placeholder="更新这条视频的简介"></textarea>
      <label style="margin-top:12px;">重新上传视频源</label>
      <input id="editVideoUrl" type="hidden" />
      <div id="editVideoUploadStatus" class="readonly">当前未更换视频源，直接保存只会更新简介。</div>
      <div class="upload-row" style="margin-top: 8px;">
        <input id="editVideoFile" type="file" accept=".mp4,.webm,.ogg" />
        <button id="btnUploadEditVideo" class="secondary" type="button">上传新视频</button>
      </div>
      <div class="auth-switch">
        <button id="btnSaveVideoEdit" type="button">保存修改</button>
        <button id="btnDeleteVideo" class="danger" type="button">删除作品</button>
      </div>
    </div>
  </div>
  <div id="avatarCropperBackdrop" class="cropper-backdrop">
    <div class="cropper-modal">
      <div class="detail-head">
        <h3>裁剪头像</h3>
        <button id="btnCloseAvatarCropper" class="secondary" type="button">关闭</button>
      </div>
      <div class="hint">拖动图片调整位置，使用滑杆缩放后再上传。</div>
      <div id="avatarCropperBox" class="cropper-box">
        <img id="avatarCropperImage" class="cropper-image" alt="avatar cropper" />
        <div class="cropper-mask"></div>
      </div>
      <label>缩放</label>
      <input id="avatarCropperZoom" type="range" min="100" max="250" step="1" value="100" />
      <div class="auth-switch">
        <button id="btnConfirmAvatarCrop" type="button">裁剪并上传头像</button>
        <button id="btnCancelAvatarCrop" class="secondary" type="button">取消</button>
      </div>
    </div>
  </div>

  <script>
    const out = document.getElementById("out");
    const toastEl = document.getElementById("toast");
    const searchInputEl = document.getElementById("searchInput");
    const userStatusEl = document.getElementById("userStatus");
    const loginBannerEl = document.getElementById("loginBanner");
    const recommendCardsEl = document.getElementById("recommendCards");
    const hotCardsEl = document.getElementById("hotCards");
    const publishCardsEl = document.getElementById("publishCards");
    const accountMyVideosEl = document.getElementById("accountMyVideos");
    const followingListEl = document.getElementById("followingList");
    const followingCountEl = document.getElementById("followingCount");
    const tabRecommendEl = document.getElementById("tabRecommend");
    const tabHotEl = document.getElementById("tabHot");
    const tabPublishEl = document.getElementById("tabPublish");
    const tabAccountEl = document.getElementById("tabAccount");
    const panelRecommendEl = document.getElementById("panelRecommend");
    const panelHotEl = document.getElementById("panelHot");
    const panelPublishEl = document.getElementById("panelPublish");
    const panelAccountEl = document.getElementById("panelAccount");
    const accountGuestEl = document.getElementById("accountGuest");
    const accountAuthedEl = document.getElementById("accountAuthed");
    const guestLoginEl = document.getElementById("guestLogin");
    const guestRegisterEl = document.getElementById("guestRegister");
    const guestRecoverEl = document.getElementById("guestRecover");
    const publicProfilePageEl = document.getElementById("publicProfilePage");
    const publicProfileRouteEl = document.getElementById("publicProfileRoute");
    const publicProfileAvatarEl = document.getElementById("publicProfileAvatar");
    const publicProfileNameEl = document.getElementById("publicProfileName");
    const publicProfileSubEl = document.getElementById("publicProfileSub");
    const publicProfileSignatureEl = document.getElementById("publicProfileSignature");
    const publicProfileFollowingEl = document.getElementById("publicProfileFollowing");
    const publicProfileFollowersEl = document.getElementById("publicProfileFollowers");
    const publicProfileLikesEl = document.getElementById("publicProfileLikes");
    const publicProfileWorksEl = document.getElementById("publicProfileWorks");
    const detailSheetBackdropEl = document.getElementById("detailSheetBackdrop");
    const editVideoBackdropEl = document.getElementById("editVideoBackdrop");
    const detailSheetAuthorEl = document.getElementById("detailSheetAuthor");
    const detailSheetTimeEl = document.getElementById("detailSheetTime");
    const detailSheetVideoEl = document.getElementById("detailSheetVideo");
    const detailSheetCoverEl = document.getElementById("detailSheetCover");
    const detailSheetDescEl = document.getElementById("detailSheetDesc");
    const avatarCropperBackdropEl = document.getElementById("avatarCropperBackdrop");
    const avatarCropperBoxEl = document.getElementById("avatarCropperBox");
    const avatarCropperImageEl = document.getElementById("avatarCropperImage");
    const avatarCropperZoomEl = document.getElementById("avatarCropperZoom");

    let activeTab = "recommend";
    let guestView = "login";
    let currentUser = null;
    let followingProfiles = [];
    let currentPublicProfileUserId = "";
    let recommendItems = [];
    let hotItems = [];
    let myVideoItems = [];
    let editingVideoId = "";
    const followedAuthors = new Set();
    let toastTimer = null;
    const DEFAULT_AVATAR = "data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' width='160' height='160' viewBox='0 0 160 160'><rect width='160' height='160' rx='32' fill='%23cbd5e1'/><circle cx='80' cy='60' r='28' fill='%23f8fafc'/><path d='M36 132c10-26 31-40 44-40s34 14 44 40' fill='%23f8fafc'/></svg>";
    const avatarCropState = {
      img: null,
      src: "",
      naturalWidth: 0,
      naturalHeight: 0,
      baseScale: 1,
      scale: 1,
      left: 0,
      top: 0,
      dragging: false,
      dragStartX: 0,
      dragStartY: 0,
      startLeft: 0,
      startTop: 0,
    };

    function byId(id) { return document.getElementById(id); }
    function valueOf(id) { return (byId(id).value || "").trim(); }
    function searchQuery() { return valueOf("searchInput"); }

    function optionalAge(id) {
      const raw = valueOf(id);
      if (!raw) return undefined;
      const v = Number(raw);
      if (!Number.isFinite(v)) return undefined;
      return Math.trunc(v);
    }

    function formatTimestamp(ms) {
      if (typeof ms !== "number") return ms;
      const d = new Date(ms);
      if (Number.isNaN(d.getTime())) return ms;
      const pad = (n) => String(n).padStart(2, "0");
      return d.getFullYear() + "-" + pad(d.getMonth() + 1) + "-" + pad(d.getDate()) + " " + pad(d.getHours()) + ":" + pad(d.getMinutes()) + ":" + pad(d.getSeconds());
    }

    function decorate(obj) {
      if (Array.isArray(obj)) return obj.map(decorate);
      if (obj && typeof obj === "object") {
        const copy = {};
        for (const [k, v] of Object.entries(obj)) {
          copy[k] = decorate(v);
          if (k === "publish_time" && typeof v === "number") copy.publish_time_text = formatTimestamp(v);
        }
        return copy;
      }
      return obj;
    }

    function showToast(message) {
      toastEl.textContent = message;
      toastEl.classList.add("show");
      if (toastTimer) clearTimeout(toastTimer);
      toastTimer = setTimeout(() => toastEl.classList.remove("show"), 1800);
    }

    function formatDuration(seconds) {
      if (!Number.isFinite(seconds) || seconds < 0) return "00:00";
      const total = Math.floor(seconds);
      const m = String(Math.floor(total / 60)).padStart(2, "0");
      const s = String(total % 60).padStart(2, "0");
      return m + ":" + s;
    }

    function summarizeDescription(text) {
      const raw = (text || "").trim();
      if (!raw) return { text: "", truncated: false };
      if (raw.length <= 30) return { text: raw, truncated: false };
      return { text: raw.slice(0, 30) + "...", truncated: true };
    }

    function openDetailSheet(item) {
      detailSheetAuthorEl.textContent = "@" + (item.author_nickname || item.author_id || "-");
      detailSheetTimeEl.textContent = item.publish_time_text || formatTimestamp(item.publish_time);
      detailSheetDescEl.textContent = item.description || "这个视频暂时还没有简介。";

      if (item.video_url) {
        detailSheetVideoEl.src = item.video_url;
        detailSheetVideoEl.classList.remove("hidden");
      } else {
        detailSheetVideoEl.pause();
        detailSheetVideoEl.removeAttribute("src");
        detailSheetVideoEl.load();
        detailSheetVideoEl.classList.add("hidden");
      }

      if (!item.video_url && item.cover_url) {
        detailSheetCoverEl.src = item.cover_url;
        detailSheetCoverEl.classList.remove("hidden");
      } else {
        detailSheetCoverEl.removeAttribute("src");
        detailSheetCoverEl.classList.add("hidden");
      }

      detailSheetBackdropEl.classList.add("show");
    }

    function closeDetailSheet() {
      detailSheetBackdropEl.classList.remove("show");
      detailSheetVideoEl.pause();
    }

    function clampAvatarPosition() {
      const viewport = avatarCropperBoxEl.clientWidth || 320;
      const width = avatarCropState.naturalWidth * avatarCropState.baseScale * avatarCropState.scale;
      const height = avatarCropState.naturalHeight * avatarCropState.baseScale * avatarCropState.scale;
      const minLeft = Math.min(0, viewport - width);
      const minTop = Math.min(0, viewport - height);
      const maxLeft = 0;
      const maxTop = 0;
      avatarCropState.left = Math.min(maxLeft, Math.max(minLeft, avatarCropState.left));
      avatarCropState.top = Math.min(maxTop, Math.max(minTop, avatarCropState.top));
    }

    function renderAvatarCropper() {
      if (!avatarCropState.img) return;
      const width = avatarCropState.naturalWidth * avatarCropState.baseScale * avatarCropState.scale;
      const height = avatarCropState.naturalHeight * avatarCropState.baseScale * avatarCropState.scale;
      clampAvatarPosition();
      avatarCropperImageEl.src = avatarCropState.src;
      avatarCropperImageEl.style.width = width + "px";
      avatarCropperImageEl.style.height = height + "px";
      avatarCropperImageEl.style.left = avatarCropState.left + "px";
      avatarCropperImageEl.style.top = avatarCropState.top + "px";
    }

    function closeAvatarCropper() {
      avatarCropperBackdropEl.classList.remove("show");
      avatarCropperBoxEl.classList.remove("dragging");
      avatarCropState.dragging = false;
    }

    function openAvatarCropper(file) {
      if (!file) {
        showToast("请先选择头像图片");
        return;
      }
      const reader = new FileReader();
      reader.onload = () => {
        const img = new Image();
        img.onload = () => {
          const viewport = avatarCropperBoxEl.clientWidth || 320;
          avatarCropState.img = img;
          avatarCropState.src = reader.result;
          avatarCropState.naturalWidth = img.naturalWidth;
          avatarCropState.naturalHeight = img.naturalHeight;
          avatarCropState.baseScale = Math.max(viewport / img.naturalWidth, viewport / img.naturalHeight);
          avatarCropState.scale = 1;
          avatarCropperZoomEl.value = "100";
          const width = img.naturalWidth * avatarCropState.baseScale;
          const height = img.naturalHeight * avatarCropState.baseScale;
          avatarCropState.left = (viewport - width) / 2;
          avatarCropState.top = (viewport - height) / 2;
          renderAvatarCropper();
          avatarCropperBackdropEl.classList.add("show");
        };
        img.src = reader.result;
      };
      reader.readAsDataURL(file);
    }

    async function uploadAvatarCropped() {
      if (!avatarCropState.img) return;
      const viewport = avatarCropperBoxEl.clientWidth || 320;
      const width = avatarCropState.naturalWidth * avatarCropState.baseScale * avatarCropState.scale;
      const height = avatarCropState.naturalHeight * avatarCropState.baseScale * avatarCropState.scale;
      const sx = Math.max(0, (-avatarCropState.left / width) * avatarCropState.naturalWidth);
      const sy = Math.max(0, (-avatarCropState.top / height) * avatarCropState.naturalHeight);
      const sw = Math.min(avatarCropState.naturalWidth, (viewport / width) * avatarCropState.naturalWidth);
      const sh = Math.min(avatarCropState.naturalHeight, (viewport / height) * avatarCropState.naturalHeight);

      const canvas = document.createElement("canvas");
      canvas.width = 256;
      canvas.height = 256;
      const ctx = canvas.getContext("2d");
      ctx.drawImage(avatarCropState.img, sx, sy, sw, sh, 0, 0, 256, 256);

      const blob = await new Promise((resolve) => canvas.toBlob(resolve, "image/png", 0.92));
      if (!blob) {
        showToast("头像裁剪失败");
        return;
      }

      const form = new FormData();
      form.append("file", blob, "avatar.png");
      const resp = await fetch("/upload/media?kind=avatar", {
        method: "POST",
        body: form,
      });
      const data = await resp.json().catch(() => ({ code: 999, msg: "invalid json response" }));
      setOutput(data);
      if (data.code === 0 && data.data && data.data.url) {
        byId("profileAvatarUrl").value = data.data.url;
        byId("profileAvatarPreview").src = data.data.url;
        closeAvatarCropper();
        showToast("头像裁剪上传成功，记得保存资料");
      }
    }

    function downloadVideo(url, filename) {
      if (!url) {
        showToast("当前视频暂不支持下载");
        return;
      }
      const link = document.createElement("a");
      link.href = url;
      link.download = filename || "video";
      document.body.appendChild(link);
      link.click();
      link.remove();
    }

    function syncVisibleVideos(container) {
      if (container._videoObserver) {
        container._videoObserver.disconnect();
        container._videoObserver = null;
      }
      const videos = Array.from(container.querySelectorAll(".video-player"));
      if (!videos.length) return;

      const observer = new IntersectionObserver((entries) => {
        entries.forEach((entry) => {
          const video = entry.target;
          if (entry.isIntersecting && entry.intersectionRatio >= 0.6) {
            video.play().catch(() => {});
            return;
          }
          video.pause();
        });
      }, {
        root: container,
        threshold: [0.25, 0.6, 0.85],
      });

      videos.forEach((video, index) => {
        observer.observe(video);
        if (index > 0) video.pause();
      });
      videos[0].play().catch(() => {});
      container._videoObserver = observer;
    }

    function updateCardFollowButton(button) {
      if (!button) return;
      const targetUserId = button.dataset.followCardUserId || "";
      const targetLabel = button.dataset.followCardLabel || targetUserId;
      const isSelf = !!(currentUser && currentUser.username && targetUserId === currentUser.username);
      if (!targetUserId || isSelf) {
        button.classList.add("hidden");
        return;
      }
      button.classList.remove("hidden");
      button.disabled = false;
      const followed = followedAuthors.has(targetUserId);
      button.classList.toggle("is-following", followed);
      button.textContent = followed ? "已关注" : "关注";
      button.title = followed ? ("取消关注 @" + targetLabel) : ("关注 @" + targetLabel);
    }

    function currentProfilePathUser() {
      const prefix = "/profile/";
      if (!location.pathname.startsWith(prefix)) return "";
      const raw = location.pathname.slice(prefix.length).split("/")[0];
      if (!raw) return "";
      try {
        return decodeURIComponent(raw);
      } catch (_) {
        return raw;
      }
    }

    function renderFollowingProfiles() {
      if (!followingListEl || !followingCountEl) return;
      followingCountEl.textContent = String(followingProfiles.length);
      followingListEl.replaceChildren();
      if (!followingProfiles.length) {
        const empty = document.createElement("div");
        empty.className = "empty";
        empty.textContent = currentUser ? "当前还没有关注任何账号。" : "登录后，这里会显示你关注的账号昵称和头像。";
        followingListEl.appendChild(empty);
        return;
      }

      followingProfiles.forEach((item) => {
        const row = document.createElement("div");
        row.className = "account-follow-item";

        const avatar = document.createElement("img");
        avatar.className = "account-follow-avatar";
        avatar.src = item.avatar_url || DEFAULT_AVATAR;
        avatar.alt = item.nickname || item.username || "avatar";
        avatar.addEventListener("click", () => openPublicProfile(item.username));
        row.appendChild(avatar);

        const info = document.createElement("div");
        info.style.minWidth = "0";
        const name = document.createElement("div");
        name.className = "account-follow-name";
        name.textContent = item.nickname || item.username || "-";
        name.addEventListener("click", () => openPublicProfile(item.username));
        info.appendChild(name);
        row.appendChild(info);

        followingListEl.appendChild(row);
      });
    }

    function mergeVideoIntoState(list, detail) {
      const next = [detail];
      list.forEach((item) => {
        if (item.author_id === detail.author_id && item.video_id === detail.video_id) return;
        next.push(item);
      });
      return next;
    }

    function updateVideoLikeInCards(authorId, videoId, likeCount) {
      myVideoItems = myVideoItems.map((item) => {
        if (currentUser && currentUser.username === authorId && item.video_id === videoId) {
          return Object.assign({}, item, { like_count: likeCount });
        }
        return item;
      });
      renderMyVideos(myVideoItems);
      if (currentPublicProfileUserId === authorId) loadPublicProfile(authorId);
    }

    async function openVideoInFeed(authorId, videoId) {
      if (!authorId || !videoId) return;
      const data = await getJSON("/video/detail?author_id=" + encodeURIComponent(authorId) + "&video_id=" + encodeURIComponent(videoId));
      setOutput(data);
      if (data.code !== 0 || !data.data) {
        showToast(data.msg || "打开视频失败");
        return;
      }
      const detail = decorate(data.data);
      recommendItems = mergeVideoIntoState(recommendItems, detail);
      renderCards(recommendCardsEl, recommendItems);
      setActiveTab("recommend");
      if (publicProfilePageEl.classList.contains("show")) closePublicProfile();
      showToast("已跳转到对应视频");
    }

    async function toggleVideoLike(item, triggerBtn) {
      if (!currentUser || !currentUser.username) {
        setActiveTab("account");
        showToast("请先登录后再点赞");
        return;
      }
      if (!item || !item.author_id || !item.video_id) return;
      if (triggerBtn) triggerBtn.disabled = true;
      const data = await postJSON(item.liked ? "/video/unlike" : "/video/like", {
        author_id: item.author_id,
        video_id: item.video_id,
      });
      setOutput(data);
      if (triggerBtn) triggerBtn.disabled = false;
      if (data.code !== 0 || !data.data) {
        showToast(data.msg || "点赞失败");
        return;
      }
      const detail = decorate(data.data);
      item.like_count = detail.like_count;
      item.liked = detail.liked;
      triggerBtn.innerHTML = "<span>❤</span><span>" + String(detail.like_count || 0) + "</span>";
      triggerBtn.classList.toggle("secondary", !detail.liked);
      updateVideoLikeInCards(detail.author_id, detail.video_id, detail.like_count || 0);
      if (detail.author_id === currentPublicProfileUserId) loadPublicProfile(detail.author_id);
      showToast(detail.liked ? "点赞成功" : "已取消点赞");
    }

    function closeEditVideoModal() {
      editingVideoId = "";
      editVideoBackdropEl.classList.remove("show");
      byId("editVideoCoverPreview").src = DEFAULT_AVATAR;
      byId("editVideoCoverUrl").value = "";
      byId("editVideoCoverStatus").textContent = "当前未更换封面，直接保存会保留原封面。";
      byId("editCoverFile").value = "";
      byId("editVideoDescription").value = "";
      byId("editVideoUrl").value = "";
      byId("editVideoUploadStatus").textContent = "当前未更换视频源，直接保存只会更新简介。";
      byId("editVideoFile").value = "";
    }

    async function openEditVideoModal(videoId) {
      if (!currentUser || !currentUser.username || !videoId) return;
      const data = await getJSON("/video/detail?author_id=" + encodeURIComponent(currentUser.username) + "&video_id=" + encodeURIComponent(videoId));
      setOutput(data);
      if (data.code !== 0 || !data.data) {
        showToast(data.msg || "加载作品信息失败");
        return;
      }
      const detail = decorate(data.data);
      editingVideoId = detail.video_id || "";
      byId("editVideoMeta").textContent = "作品 " + (detail.video_id || "") + " · 发布于 " + (detail.publish_time_text || formatTimestamp(detail.publish_time));
      byId("editVideoCoverPreview").src = detail.cover_url || DEFAULT_AVATAR;
      byId("editVideoCoverUrl").value = detail.cover_url || "";
      byId("editVideoCoverStatus").textContent = detail.cover_url ? "当前封面已存在，可直接保留或重新上传。" : "当前未检测到封面，可重新上传。";
      byId("editVideoDescription").value = detail.description || "";
      byId("editVideoUrl").value = detail.video_url || "";
      byId("editVideoUploadStatus").textContent = detail.video_url ? "当前视频源已存在，可直接保存简介或重新上传。" : "当前未检测到视频源，可重新上传。";
      editVideoBackdropEl.classList.add("show");
    }

    async function uploadEditVideoCover() {
      const input = byId("editCoverFile");
      if (!input.files || !input.files.length) {
        showToast("请先选择新封面");
        return;
      }
      const form = new FormData();
      form.append("file", input.files[0]);
      const resp = await fetch("/upload/media?kind=cover", { method: "POST", body: form });
      const data = await resp.json().catch(() => ({ code: 999, msg: "invalid json response" }));
      setOutput(data);
      if (data.code === 0 && data.data && data.data.url) {
        byId("editVideoCoverUrl").value = data.data.url;
        byId("editVideoCoverPreview").src = data.data.url;
        byId("editVideoCoverStatus").textContent = "新封面上传成功：" + (data.data.filename || data.data.url);
        showToast("新封面上传成功");
      }
    }

    async function uploadEditVideoSource() {
      const input = byId("editVideoFile");
      if (!input.files || !input.files.length) {
        showToast("请先选择新视频文件");
        return;
      }
      const form = new FormData();
      form.append("file", input.files[0]);
      const resp = await fetch("/upload/media?kind=video", { method: "POST", body: form });
      const data = await resp.json().catch(() => ({ code: 999, msg: "invalid json response" }));
      setOutput(data);
      if (data.code === 0 && data.data && data.data.url) {
        byId("editVideoUrl").value = data.data.url;
        byId("editVideoUploadStatus").textContent = "新视频上传成功：" + (data.data.filename || data.data.url);
        showToast("新视频源上传成功");
      }
    }

    async function saveVideoEdit() {
      if (!editingVideoId) return;
      const data = await postJSON("/video/update", {
        video_id: editingVideoId,
        cover_url: valueOf("editVideoCoverUrl"),
        description: valueOf("editVideoDescription"),
        video_url: valueOf("editVideoUrl"),
      });
      setOutput(data);
      if (data.code !== 0) {
        showToast(data.msg || "保存作品失败");
        return;
      }
      closeEditVideoModal();
      await loadMyVideos();
      await loadRecommend();
      await loadHot();
      if (currentPublicProfileUserId) await loadPublicProfile(currentPublicProfileUserId);
      showToast("作品已更新");
    }

    async function deleteVideoByEditing() {
      if (!editingVideoId) return;
      if (!confirm("确认删除这条作品吗？删除后无法恢复。")) return;
      const data = await postJSON("/video/delete", { video_id: editingVideoId });
      setOutput(data);
      if (data.code !== 0) {
        showToast(data.msg || "删除作品失败");
        return;
      }
      closeEditVideoModal();
      await loadMyVideos();
      await loadRecommend();
      await loadHot();
      if (currentPublicProfileUserId) await loadPublicProfile(currentPublicProfileUserId);
      showToast("作品已删除");
    }

    function renderMyVideos(items) {
      myVideoItems = items.slice();
      accountMyVideosEl.replaceChildren();
      if (!items.length) {
        const empty = document.createElement("div");
        empty.className = "empty";
        empty.textContent = currentUser ? "你还没有发布过视频。" : "登录后，这里会显示你发布过的视频。";
        accountMyVideosEl.appendChild(empty);
        return;
      }

      items.forEach((item) => {
        const card = document.createElement("div");
        card.className = "published-item";
        card.addEventListener("click", () => {
          if (!currentUser || !currentUser.username) return;
          openVideoInFeed(currentUser.username, item.video_id);
        });
        const gear = document.createElement("button");
        gear.className = "published-gear";
        gear.type = "button";
        gear.textContent = "⚙";
        gear.addEventListener("click", (event) => {
          event.stopPropagation();
          openEditVideoModal(item.video_id);
        });
        card.appendChild(gear);
        const cover = document.createElement("img");
        cover.className = "published-cover";
        cover.src = item.cover_url || DEFAULT_AVATAR;
        cover.alt = item.video_id || "cover";
        card.appendChild(cover);
        const meta = document.createElement("div");
        meta.className = "published-meta";
        const like = document.createElement("span");
        like.className = "published-like";
        like.textContent = "❤ " + String(item.like_count || 0);
        meta.appendChild(like);
        card.appendChild(meta);
        accountMyVideosEl.appendChild(card);
      });
    }

    function syncFollowUI() {
      document.querySelectorAll("[data-follow-card-user-id]").forEach((button) => updateCardFollowButton(button));
      renderFollowingProfiles();
    }

    async function refreshFollowingAuthors() {
      followedAuthors.clear();
      followingProfiles = [];
      if (!currentUser || !currentUser.username) {
        syncFollowUI();
        return;
      }
      const data = await getJSON("/account/following_profiles?user_id=" + encodeURIComponent(currentUser.username));
      if (data.code === 0 && Array.isArray(data.data)) {
        followingProfiles = decorate(data.data);
        followingProfiles.forEach((item) => {
          if (item && item.username) followedAuthors.add(item.username);
        });
      }
      syncFollowUI();
    }

    async function loadMyVideos() {
      if (!currentUser || !currentUser.username) {
        renderMyVideos([]);
        return;
      }
      const data = await getJSON("/account/my_videos");
      setOutput(data);
      renderMyVideos(Array.isArray(data.data) ? decorate(data.data) : []);
    }

    function renderPublicProfile(profile) {
      publicProfileRouteEl.textContent = "/profile/" + (profile.username || "");
      publicProfileAvatarEl.src = profile.avatar_url || DEFAULT_AVATAR;
      publicProfileNameEl.textContent = profile.nickname || profile.username || "创作者";
      const subBits = [];
      if (profile.age !== undefined && profile.age !== null && profile.age !== "") subBits.push(profile.age + " 岁");
      if (profile.address) subBits.push(profile.address);
      if (!subBits.length && profile.username) subBits.push("@" + profile.username);
      publicProfileSubEl.textContent = subBits.join(" · ");
      publicProfileSignatureEl.textContent = profile.signature || "这个创作者还没有留下个人简介。";
      publicProfileFollowingEl.textContent = String(profile.following_count || 0);
      publicProfileFollowersEl.textContent = String(profile.follower_count || 0);
      publicProfileLikesEl.textContent = String(profile.like_count || 0);

      publicProfileWorksEl.replaceChildren();
      const videos = Array.isArray(profile.videos) ? profile.videos : [];
      if (!videos.length) {
        const empty = document.createElement("div");
        empty.className = "empty";
        empty.textContent = "Ta 还没有发布作品。";
        publicProfileWorksEl.appendChild(empty);
        return;
      }
      videos.forEach((item) => {
        const card = document.createElement("div");
        card.className = "public-profile-work";
        card.addEventListener("click", () => openVideoInFeed(profile.username, item.video_id));
        const cover = document.createElement("img");
        cover.className = "public-profile-work-cover";
        cover.src = item.cover_url || DEFAULT_AVATAR;
        cover.alt = item.video_id || "cover";
        card.appendChild(cover);
        const meta = document.createElement("div");
        meta.className = "public-profile-work-meta";
        const left = document.createElement("span");
        left.textContent = item.publish_time_text || formatTimestamp(item.publish_time);
        meta.appendChild(left);
        const right = document.createElement("span");
        right.textContent = "❤ " + String(item.like_count || 0);
        meta.appendChild(right);
        card.appendChild(meta);
        publicProfileWorksEl.appendChild(card);
      });
    }

    async function loadPublicProfile(userId) {
      currentPublicProfileUserId = userId || "";
      publicProfilePageEl.classList.add("show");
      publicProfileRouteEl.textContent = "/profile/" + (userId || "");
      publicProfileNameEl.textContent = "加载中...";
      publicProfileSubEl.textContent = "";
      publicProfileSignatureEl.textContent = "正在加载创作者信息...";
      publicProfileAvatarEl.src = DEFAULT_AVATAR;
      publicProfileFollowingEl.textContent = "0";
      publicProfileFollowersEl.textContent = "0";
      publicProfileLikesEl.textContent = "0";
      publicProfileWorksEl.replaceChildren();
      const loading = document.createElement("div");
      loading.className = "empty";
      loading.textContent = "正在加载作品...";
      publicProfileWorksEl.appendChild(loading);

      const data = await getJSON("/profile/data?user_id=" + encodeURIComponent(userId));
      setOutput(data);
      if (data.code === 0 && data.data) {
        renderPublicProfile(decorate(data.data));
        return;
      }
      publicProfileNameEl.textContent = "未找到该创作者";
      publicProfileSignatureEl.textContent = data.msg || "暂时无法加载该页面。";
      publicProfileWorksEl.replaceChildren();
      const empty = document.createElement("div");
      empty.className = "empty";
      empty.textContent = "没有可展示的作品。";
      publicProfileWorksEl.appendChild(empty);
    }

    async function openPublicProfile(userId) {
      if (!userId) return;
      const nextPath = "/profile/" + encodeURIComponent(userId);
      if (location.pathname !== nextPath) {
        history.pushState({ profileUserId: userId }, "", nextPath);
      }
      await loadPublicProfile(userId);
    }

    function closePublicProfile() {
      currentPublicProfileUserId = "";
      publicProfilePageEl.classList.remove("show");
      if (location.pathname.startsWith("/profile/")) {
        history.pushState({}, "", "/");
      }
    }

    async function toggleFollowAuthor(targetUserId, targetLabel, triggerBtn) {
      if (!currentUser || !currentUser.username) {
        setActiveTab("account");
        showToast("请先登录后再关注");
        return;
      }
      if (!targetUserId || targetUserId === currentUser.username) return;

      const followed = followedAuthors.has(targetUserId);
      if (triggerBtn) triggerBtn.disabled = true;
      const data = await postJSON(followed ? "/unfollow" : "/follow", {
        user_id: currentUser.username,
        target_user_id: targetUserId,
      });
      setOutput(data);
      if (data.code === 0) {
        if (followed) {
          followedAuthors.delete(targetUserId);
          showToast("已取消关注 @" + targetLabel);
        } else {
          followedAuthors.add(targetUserId);
          showToast("已关注 @" + targetLabel);
        }
        await refreshFollowingAuthors();
        return;
      }
      if (triggerBtn) triggerBtn.disabled = false;
      showToast(data.msg || (followed ? "取消关注失败" : "关注失败"));
    }

    function renderCards(container, videos) {
      if (container._videoObserver) {
        container._videoObserver.disconnect();
        container._videoObserver = null;
      }
      container.replaceChildren();
      if (!videos.length) {
        const empty = document.createElement("div");
        empty.className = "empty";
        empty.textContent = "当前结果没有可展示的视频内容。";
        container.appendChild(empty);
        return;
      }
      for (const item of videos) {
        const wrap = document.createElement("div");
        wrap.className = "video-item";

        const stage = document.createElement("div");
        stage.className = "video-stage";

        let video = null;
        if (item.video_url) {
          video = document.createElement("video");
          video.className = "video-player";
          video.src = item.video_url;
          video.controls = false;
          video.preload = "metadata";
          video.muted = true;
          video.volume = 0.2;
          video.loop = true;
          video.playsInline = true;
          video.autoplay = true;
          video.addEventListener("volumechange", () => {
            if (!video.muted && !video.dataset.adjustedVolume) {
              video.volume = 0.2;
              video.dataset.adjustedVolume = "1";
            }
          });
          stage.appendChild(video);
        } else if (item.cover_url) {
          const cover = document.createElement("img");
          cover.className = "video-cover";
          cover.src = item.cover_url;
          cover.alt = item.video_id || "cover";
          stage.appendChild(cover);
        }

        const actionRail = document.createElement("div");
        actionRail.className = "video-action-rail";
        const likeBtn = document.createElement("button");
        likeBtn.className = "action-btn" + (item.liked ? "" : " secondary");
        likeBtn.type = "button";
        likeBtn.innerHTML = "<span>❤</span><span>" + String(item.like_count || 0) + "</span>";
        likeBtn.addEventListener("click", () => toggleVideoLike(item, likeBtn));
        actionRail.appendChild(likeBtn);
        [["💬", "评论"], ["★", "收藏"]].forEach(([icon, label]) => {
          const btn = document.createElement("button");
          btn.className = "action-btn";
          btn.type = "button";
          btn.innerHTML = "<span>" + icon + "</span><span>" + label + "</span>";
          btn.addEventListener("click", () => showToast(label + "功能后续再接"));
          actionRail.appendChild(btn);
        });
        stage.appendChild(actionRail);

        const overlay = document.createElement("div");
        overlay.className = "video-overlay";

        const bottomBar = document.createElement("div");
        bottomBar.className = "video-bottom-bar";

        const infoRow = document.createElement("div");
        infoRow.className = "video-info-row";
        const infoLeft = document.createElement("div");
        infoLeft.className = "video-info-left";
        const avatarWrap = document.createElement("div");
        avatarWrap.className = "avatar-wrap";
        const avatar = document.createElement("img");
        avatar.className = "avatar";
        avatar.src = item.author_avatar || DEFAULT_AVATAR;
        avatar.alt = item.author_nickname || item.author_id || "avatar";
        avatar.classList.add("clickable-profile");
        avatar.addEventListener("click", () => openPublicProfile(item.author_id));
        avatarWrap.appendChild(avatar);
        const followBtn = document.createElement("button");
        followBtn.className = "avatar-follow-btn";
        followBtn.type = "button";
        followBtn.dataset.followCardUserId = item.author_id || "";
        followBtn.dataset.followCardLabel = item.author_nickname || item.author_id || "";
        updateCardFollowButton(followBtn);
        followBtn.addEventListener("click", () => toggleFollowAuthor(item.author_id, item.author_nickname || item.author_id, followBtn));
        avatarWrap.appendChild(followBtn);
        infoLeft.appendChild(avatarWrap);

        const infoText = document.createElement("div");
        infoText.className = "video-info-text";
        const authorTitle = document.createElement("div");
        authorTitle.className = "video-author";
        authorTitle.textContent = "@" + (item.author_nickname || item.author_id || "-");
        authorTitle.classList.add("clickable-profile");
        authorTitle.addEventListener("click", (event) => {
          event.stopPropagation();
          openPublicProfile(item.author_id);
        });
        infoText.appendChild(authorTitle);
        const desc = document.createElement("div");
        desc.className = "video-desc-inline";
        desc.textContent = item.description || "这个视频暂时还没有简介";
        infoText.appendChild(desc);
        infoText.addEventListener("click", () => openDetailSheet(item));
        infoLeft.appendChild(infoText);
        infoRow.appendChild(infoLeft);
        const spacer = document.createElement("div");
        spacer.className = "video-info-spacer";
        infoRow.appendChild(spacer);
        bottomBar.appendChild(infoRow);

        const progressRow = document.createElement("div");
        progressRow.className = "progress-row-only";
        const progressBar = document.createElement("input");
        progressBar.type = "range";
        progressBar.className = "video-progress-top";
        progressBar.min = "0";
        progressBar.max = "1000";
        progressBar.step = "1";
        progressBar.value = "0";
        progressBar.disabled = !video;
        progressRow.appendChild(progressBar);
        bottomBar.appendChild(progressRow);

        const controlsRow = document.createElement("div");
        controlsRow.className = "video-controls-row";
        const leftControls = document.createElement("div");
        leftControls.className = "video-controls-left";
        const playBtn = document.createElement("button");
        playBtn.className = "video-icon-btn";
        playBtn.type = "button";
        playBtn.textContent = video ? "⏸" : "▶";
        leftControls.appendChild(playBtn);
        const timeLabel = document.createElement("div");
        timeLabel.className = "time-label";
        timeLabel.textContent = "00:00 / 00:00";
        leftControls.appendChild(timeLabel);
        controlsRow.appendChild(leftControls);

        const rightControls = document.createElement("div");
        rightControls.className = "video-controls-right";
        const muteBtn = document.createElement("button");
        muteBtn.className = "video-icon-btn";
        muteBtn.type = "button";
        muteBtn.textContent = "🔇";
        muteBtn.disabled = !video;
        rightControls.appendChild(muteBtn);
        const volumeSlider = document.createElement("input");
        volumeSlider.type = "range";
        volumeSlider.className = "volume-slider";
        volumeSlider.min = "0";
        volumeSlider.max = "100";
        volumeSlider.step = "1";
        volumeSlider.value = "20";
        volumeSlider.disabled = !video;
        rightControls.appendChild(volumeSlider);

        const moreWrap = document.createElement("div");
        moreWrap.className = "more-wrap";
        const moreBtn = document.createElement("button");
        moreBtn.className = "video-icon-btn";
        moreBtn.type = "button";
        moreBtn.textContent = "⋮";
        const moreMenu = document.createElement("div");
        moreMenu.className = "more-menu";
        const detailMenuItem = document.createElement("button");
        detailMenuItem.className = "more-item";
        detailMenuItem.type = "button";
        detailMenuItem.textContent = "查看详情";
        detailMenuItem.addEventListener("click", () => {
          moreMenu.classList.remove("show");
          openDetailSheet(item);
        });
        const downloadMenuItem = document.createElement("button");
        downloadMenuItem.className = "more-item";
        downloadMenuItem.type = "button";
        downloadMenuItem.textContent = "下载视频";
        downloadMenuItem.addEventListener("click", () => {
          moreMenu.classList.remove("show");
          downloadVideo(item.video_url, (item.video_id || "video") + ".mp4");
        });
        moreMenu.appendChild(detailMenuItem);
        moreMenu.appendChild(downloadMenuItem);
        moreWrap.addEventListener("click", (event) => event.stopPropagation());
        moreBtn.addEventListener("click", (event) => {
          event.stopPropagation();
          const shouldShow = !moreMenu.classList.contains("show");
          moreMenu.classList.toggle("show");
          if (shouldShow) {
            setTimeout(() => {
              document.addEventListener("click", () => moreMenu.classList.remove("show"), { once: true });
            }, 0);
          }
        });
        moreWrap.appendChild(moreBtn);
        moreWrap.appendChild(moreMenu);
        rightControls.appendChild(moreWrap);
        controlsRow.appendChild(rightControls);
        bottomBar.appendChild(controlsRow);

        overlay.appendChild(bottomBar);
        stage.appendChild(overlay);
        wrap.appendChild(stage);

        if (video) {
          const syncProgress = () => {
            const current = video.currentTime || 0;
            const duration = video.duration || 0;
            const ratio = duration > 0 ? Math.min(1000, Math.round((current / duration) * 1000)) : 0;
            progressBar.value = String(ratio);
            timeLabel.textContent = formatDuration(current) + " / " + formatDuration(duration);
            playBtn.textContent = video.paused ? "▶" : "⏸";
            muteBtn.textContent = video.muted ? "🔇" : "🔊";
            volumeSlider.value = String(Math.round((video.muted ? 0 : video.volume) * 100));
          };

          video.addEventListener("loadedmetadata", syncProgress);
          video.addEventListener("timeupdate", syncProgress);
          video.addEventListener("play", syncProgress);
          video.addEventListener("pause", syncProgress);
          progressBar.addEventListener("input", () => {
            if (!video.duration) return;
            video.currentTime = (Number(progressBar.value) / 1000) * video.duration;
          });
          playBtn.addEventListener("click", async () => {
            if (video.paused) {
              try {
                await video.play();
              } catch (_) {}
            } else {
              video.pause();
            }
            syncProgress();
          });
          muteBtn.addEventListener("click", () => {
            if (video.muted) {
              video.muted = false;
              video.volume = Math.max(0.2, video.volume || 0.2);
              video.dataset.adjustedVolume = "1";
            } else {
              video.muted = true;
            }
            syncProgress();
          });
          volumeSlider.addEventListener("input", () => {
            const value = Number(volumeSlider.value) / 100;
            video.volume = value;
            video.muted = value === 0;
            video.dataset.adjustedVolume = "1";
            syncProgress();
          });
          syncProgress();
        } else {
          playBtn.disabled = true;
          muteBtn.disabled = true;
          volumeSlider.disabled = true;
          moreBtn.disabled = false;
        }

        container.appendChild(wrap);
      }
      container.scrollTop = 0;
      syncVisibleVideos(container);
    }

    function setOutput(obj) {
      out.textContent = JSON.stringify(decorate(obj), null, 2);
    }

    async function getJSON(url) {
      const resp = await fetch(url, { method: "GET" });
      return await resp.json().catch(() => ({ code: 999, msg: "invalid json response" }));
    }

    async function postJSON(url, body) {
      const resp = await fetch(url, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      return await resp.json().catch(() => ({ code: 999, msg: "invalid json response" }));
    }

    async function uploadLocalMedia(kind, inputId, targetId) {
      const input = byId(inputId);
      if (!input.files || !input.files.length) {
        setOutput({ code: 1, msg: "请先选择文件" });
        return;
      }
      const form = new FormData();
      form.append("file", input.files[0]);
      const resp = await fetch("/upload/media?kind=" + encodeURIComponent(kind), {
        method: "POST",
        body: form,
      });
      const data = await resp.json().catch(() => ({ code: 999, msg: "invalid json response" }));
      setOutput(data);
      if (data.code === 0 && data.data && data.data.url) {
        byId(targetId).value = data.data.url;
        if (kind === "cover") {
          byId("coverUploadStatus").textContent = "封面上传成功：" + (data.data.filename || data.data.url);
        } else if (kind === "video") {
          byId("videoUploadStatus").textContent = "视频上传成功：" + (data.data.filename || data.data.url);
        } else if (kind === "avatar") {
          byId("profileAvatarUrl").value = data.data.url;
          byId("profileAvatarPreview").src = data.data.url;
        }
        showToast((kind === "avatar" ? "头像" : kind === "cover" ? "封面" : "视频") + "上传成功");
      }
    }

    function setActiveTab(next) {
      activeTab = next;
      const tabMap = {
        recommend: [tabRecommendEl, panelRecommendEl],
        hot: [tabHotEl, panelHotEl],
        publish: [tabPublishEl, panelPublishEl],
        account: [tabAccountEl, panelAccountEl],
      };
      for (const [name, [tab, panel]] of Object.entries(tabMap)) {
        tab.classList.toggle("active", name === next);
        panel.classList.toggle("active", name === next);
      }
    }

    function setGuestView(view) {
      guestView = view;
      guestLoginEl.classList.toggle("hidden", view !== "login");
      guestRegisterEl.classList.toggle("hidden", view !== "register");
      guestRecoverEl.classList.toggle("hidden", view !== "recover");
    }

    function fillProfileForm(user) {
      byId("profileUsername").textContent = user.username || "";
      byId("currentUsername").textContent = user.username || "";
      byId("profileNickname").value = user.nickname || "";
      byId("profileAvatarUrl").value = user.avatar_url || "";
      byId("profileAvatarPreview").src = user.avatar_url || DEFAULT_AVATAR;
      byId("profileAge").value = user.age ?? "";
      byId("profileAddress").value = user.address || "";
      byId("profileSignature").value = user.signature || "";
      byId("profileSecurityQuestion").value = user.security_question || "默认问题：请输入默认答案 123456";
      byId("profileSecurityAnswer").value = "";
    }

    function setAuthUI(user) {
      currentUser = user;
      const authed = !!user;
      userStatusEl.textContent = authed ? ("已登录: " + (user.nickname || user.username)) : "未登录";
      loginBannerEl.classList.toggle("hidden", authed);
      tabPublishEl.disabled = !authed;
      accountGuestEl.classList.toggle("hidden", authed);
      accountAuthedEl.classList.toggle("hidden", !authed);
      if (authed) {
        fillProfileForm(user);
      } else {
        setGuestView("login");
        if (activeTab === "publish") setActiveTab("account");
      }
      syncFollowUI();
    }

    async function loadCurrentUser() {
      const data = await getJSON("/account/me");
      setOutput(data);
      if (data.code === 0 && data.data) {
        setAuthUI(data.data);
      } else {
        setAuthUI(null);
      }
    }

    async function loadRecommend() {
      let url = "/recommend";
      if (searchQuery()) url += "?q=" + encodeURIComponent(searchQuery());
      const data = await getJSON(url);
      setOutput(data);
      recommendItems = Array.isArray(data.data) ? decorate(data.data) : [];
      renderCards(recommendCardsEl, recommendItems);
    }

    async function loadHot() {
      let url = "/hot";
      if (searchQuery()) url += "?q=" + encodeURIComponent(searchQuery());
      const data = await getJSON(url);
      setOutput(data);
      hotItems = Array.isArray(data.data) ? decorate(data.data) : [];
      renderCards(hotCardsEl, hotItems);
    }

    async function publishVideo() {
      if (!valueOf("videoUrl")) {
        showToast("请先上传本地视频");
        return;
      }
      const body = {
        video_id: valueOf("videoId"),
        cover_url: valueOf("coverUrl"),
        video_url: valueOf("videoUrl"),
        description: valueOf("description"),
      };
      const data = await postJSON("/publish", body);
      setOutput(data);
      if (data.code === 0 && data.data) {
        renderCards(publishCardsEl, [decorate(data.data)]);
        await loadRecommend();
        await loadHot();
        await loadMyVideos();
        await refreshFollowingAuthors();
        showToast("视频发布成功");
      }
    }

    async function login() {
      const data = await postJSON("/account/login", {
        username: valueOf("loginUsername"),
        password: valueOf("loginPassword"),
      });
      setOutput(data);
      if (data.code === 0 && data.data) {
        setAuthUI(data.data);
        await refreshFollowingAuthors();
        await loadMyVideos();
        await loadRecommend();
        await loadHot();
        setActiveTab("publish");
        showToast("登录成功");
      }
    }

    async function register() {
      const body = {
        username: valueOf("registerUsername"),
        password: valueOf("registerPassword"),
        nickname: valueOf("registerNickname"),
        address: valueOf("registerAddress"),
        signature: valueOf("registerSignature"),
        security_question: valueOf("registerSecurityQuestion"),
        security_answer: valueOf("registerSecurityAnswer"),
      };
      const age = optionalAge("registerAge");
      if (age !== undefined) body.age = age;
      const data = await postJSON("/account/register", body);
      setOutput(data);
      if (data.code === 0) {
        byId("loginUsername").value = body.username;
        byId("loginPassword").value = body.password;
        setGuestView("login");
        showToast("注册成功，请登录");
      }
    }

    async function loadSecurityQuestion() {
      const username = valueOf("resetUsername");
      const data = await getJSON("/account/security_question?username=" + encodeURIComponent(username));
      setOutput(data);
      if (data.code === 0 && data.data) {
        byId("resetQuestion").textContent = data.data.security_question || "未设置问题";
      }
    }

    async function resetPassword() {
      const data = await postJSON("/account/reset_password", {
        username: valueOf("resetUsername"),
        security_answer: valueOf("resetAnswer"),
      });
      setOutput(data);
      if (data.code === 0) {
        byId("loginUsername").value = valueOf("resetUsername");
        byId("loginPassword").value = "123456";
        setGuestView("login");
        showToast("已重置为 123456");
      }
    }

    async function saveProfile() {
      const body = {
        nickname: valueOf("profileNickname"),
        avatar_url: valueOf("profileAvatarUrl"),
        address: valueOf("profileAddress"),
        signature: valueOf("profileSignature"),
        security_question: valueOf("profileSecurityQuestion"),
        security_answer: valueOf("profileSecurityAnswer"),
      };
      const age = optionalAge("profileAge");
      if (age !== undefined) body.age = age;
      const data = await postJSON("/account/update_profile", body);
      setOutput(data);
      if (data.code === 0 && data.data) {
        setAuthUI(data.data);
        showToast("资料已保存");
      }
    }

    async function changePassword() {
      const data = await postJSON("/account/change_password", {
        old_password: valueOf("oldPassword"),
        new_password: valueOf("newPassword"),
      });
      setOutput(data);
      if (data.code === 0) {
        byId("oldPassword").value = "";
        byId("newPassword").value = "";
        showToast("密码修改成功");
      }
    }

    async function logout() {
      const data = await postJSON("/account/logout", {});
      setOutput(data);
      setAuthUI(null);
      closeEditVideoModal();
      followedAuthors.clear();
      followingProfiles = [];
      recommendItems = [];
      hotItems = [];
      myVideoItems = [];
      syncFollowUI();
      renderMyVideos([]);
      await loadRecommend();
      await loadHot();
      setActiveTab("account");
    }

    async function searchByActiveTab() {
      if (activeTab === "hot") {
        await loadHot();
        return;
      }
      await loadRecommend();
      if (activeTab === "publish" || activeTab === "account") setActiveTab("recommend");
    }

    tabRecommendEl.addEventListener("click", () => setActiveTab("recommend"));
    tabHotEl.addEventListener("click", () => setActiveTab("hot"));
    tabPublishEl.addEventListener("click", () => { if (!tabPublishEl.disabled) setActiveTab("publish"); });
    tabAccountEl.addEventListener("click", () => setActiveTab("account"));

    byId("btnSearch").addEventListener("click", () => searchByActiveTab());
    byId("btnSearchRecommend").addEventListener("click", () => loadRecommend());
    byId("btnSearchHot").addEventListener("click", () => loadHot());
    byId("btnRefreshFollowingProfiles").addEventListener("click", () => refreshFollowingAuthors());
    byId("btnRecommendRefresh").addEventListener("click", () => loadRecommend());
    byId("btnHotRefresh").addEventListener("click", () => loadHot());
    byId("btnPublish").addEventListener("click", () => publishVideo());
    byId("btnUploadCover").addEventListener("click", () => uploadLocalMedia("cover", "coverFile", "coverUrl"));
    byId("btnUploadVideo").addEventListener("click", () => uploadLocalMedia("video", "videoFile", "videoUrl"));
    byId("btnUploadAvatar").addEventListener("click", () => openAvatarCropper(byId("avatarFile").files && byId("avatarFile").files[0]));
    byId("avatarFile").addEventListener("change", () => openAvatarCropper(byId("avatarFile").files && byId("avatarFile").files[0]));
    byId("btnConfirmAvatarCrop").addEventListener("click", () => uploadAvatarCropped());
    byId("btnCancelAvatarCrop").addEventListener("click", () => closeAvatarCropper());
    byId("btnCloseAvatarCropper").addEventListener("click", () => closeAvatarCropper());
    byId("btnCloseDetail").addEventListener("click", () => closeDetailSheet());
    byId("btnCloseEditVideo").addEventListener("click", () => closeEditVideoModal());
    byId("btnUploadEditCover").addEventListener("click", () => uploadEditVideoCover());
    byId("btnUploadEditVideo").addEventListener("click", () => uploadEditVideoSource());
    byId("btnSaveVideoEdit").addEventListener("click", () => saveVideoEdit());
    byId("btnDeleteVideo").addEventListener("click", () => deleteVideoByEditing());
    byId("btnLogin").addEventListener("click", () => login());
    byId("btnRegister").addEventListener("click", () => register());
    byId("btnLoadQuestion").addEventListener("click", () => loadSecurityQuestion());
    byId("btnResetPassword").addEventListener("click", () => resetPassword());
    byId("btnSaveProfile").addEventListener("click", () => saveProfile());
    byId("btnChangePassword").addEventListener("click", () => changePassword());
    byId("btnLogout").addEventListener("click", () => logout());
    byId("btnBackFromProfile").addEventListener("click", () => closePublicProfile());
    byId("btnToRegister").addEventListener("click", () => setGuestView("register"));
    byId("btnToRecover").addEventListener("click", () => setGuestView("recover"));
    byId("btnBackToLogin1").addEventListener("click", () => setGuestView("login"));
    byId("btnBackToLogin2").addEventListener("click", () => setGuestView("login"));
    searchInputEl.addEventListener("keydown", (event) => {
      if (event.key === "Enter") {
        event.preventDefault();
        searchByActiveTab();
      }
    });
    detailSheetBackdropEl.addEventListener("click", (event) => {
      if (event.target === detailSheetBackdropEl) closeDetailSheet();
    });
    editVideoBackdropEl.addEventListener("click", (event) => {
      if (event.target === editVideoBackdropEl) closeEditVideoModal();
    });
    avatarCropperBackdropEl.addEventListener("click", (event) => {
      if (event.target === avatarCropperBackdropEl) closeAvatarCropper();
    });
    avatarCropperZoomEl.addEventListener("input", () => {
      if (!avatarCropState.img) return;
      const viewport = avatarCropperBoxEl.clientWidth || 320;
      const oldWidth = avatarCropState.naturalWidth * avatarCropState.baseScale * avatarCropState.scale;
      const oldHeight = avatarCropState.naturalHeight * avatarCropState.baseScale * avatarCropState.scale;
      const centerX = (viewport / 2 - avatarCropState.left) / oldWidth;
      const centerY = (viewport / 2 - avatarCropState.top) / oldHeight;
      avatarCropState.scale = Number(avatarCropperZoomEl.value) / 100;
      const newWidth = avatarCropState.naturalWidth * avatarCropState.baseScale * avatarCropState.scale;
      const newHeight = avatarCropState.naturalHeight * avatarCropState.baseScale * avatarCropState.scale;
      avatarCropState.left = viewport / 2 - centerX * newWidth;
      avatarCropState.top = viewport / 2 - centerY * newHeight;
      renderAvatarCropper();
    });
    avatarCropperBoxEl.addEventListener("pointerdown", (event) => {
      if (!avatarCropState.img) return;
      avatarCropState.dragging = true;
      avatarCropState.dragStartX = event.clientX;
      avatarCropState.dragStartY = event.clientY;
      avatarCropState.startLeft = avatarCropState.left;
      avatarCropState.startTop = avatarCropState.top;
      avatarCropperBoxEl.classList.add("dragging");
      avatarCropperBoxEl.setPointerCapture(event.pointerId);
    });
    avatarCropperBoxEl.addEventListener("pointermove", (event) => {
      if (!avatarCropState.dragging) return;
      avatarCropState.left = avatarCropState.startLeft + (event.clientX - avatarCropState.dragStartX);
      avatarCropState.top = avatarCropState.startTop + (event.clientY - avatarCropState.dragStartY);
      renderAvatarCropper();
    });
    const stopAvatarDrag = () => {
      avatarCropState.dragging = false;
      avatarCropperBoxEl.classList.remove("dragging");
    };
    avatarCropperBoxEl.addEventListener("pointerup", () => stopAvatarDrag());
    avatarCropperBoxEl.addEventListener("pointercancel", () => stopAvatarDrag());
    window.addEventListener("keydown", (event) => {
      if (event.key === "Escape") {
        closeDetailSheet();
        closeEditVideoModal();
        closeAvatarCropper();
        if (publicProfilePageEl.classList.contains("show")) closePublicProfile();
      }
    });
    window.addEventListener("popstate", async () => {
      const profileUser = currentProfilePathUser();
      if (profileUser) {
        await loadPublicProfile(profileUser);
        return;
      }
      currentPublicProfileUserId = "";
      publicProfilePageEl.classList.remove("show");
    });

    async function bootstrap() {
      setOutput({ code: 0, msg: "ready" });
      setGuestView("login");
      await loadCurrentUser();
      await refreshFollowingAuthors();
      await loadMyVideos();
      await loadRecommend();
      await loadHot();
      const profileUser = currentProfilePathUser();
      if (profileUser) {
        await loadPublicProfile(profileUser);
      }
    }

    bootstrap();
  </script>
</body>
</html>`
