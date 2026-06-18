package app

import (
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"blog/internal/data"
)

const (
	// maxAvatarUploadBytes 限制头像上传大小，防止单文件过大。
	maxAvatarUploadBytes = 4 << 20 // 4 MB
)

var allowedMemberBackgrounds = map[string]struct{}{
	"#f5f7f6": {},
	"#f3f4f8": {},
	"#f7f3ec": {},
	"linear-gradient(180deg, #f6f6f2, #eceff3)":                          {},
	"linear-gradient(180deg, #eef7f3, #e3efe8)":                          {},
	"linear-gradient(180deg, #f7f2e9, #efe8da)":                          {},
	"url('/static/img/paper-bg.svg') center top / cover no-repeat fixed": {},
	"url('/static/img/hero-bg.svg') center / cover no-repeat fixed":      {},
}

// memberBaseData 构建访问者后台页面的基础数据。
// 这里会覆盖 admin_background，让访问者的“外观设置”只作用于自己的后台界面。
func (a *App) memberBaseData(r *http.Request, nav string, user *data.User) ViewData {
	viewData := a.baseData(r, nav)
	viewData.User = user

	if viewData.Settings == nil {
		viewData.Settings = make(map[string]string)
	}
	bg, _ := a.store.GetUserSetting(user.ID, "member_background")
	bg = normalizeBackgroundSetting(bg, allowedMemberBackgrounds, defaultAdminBackground)
	viewData.Settings["admin_background"] = bg
	viewData.AdminBackgroundCSS = template.CSS(bg)
	return viewData
}

// handleMemberDashboard 渲染访问者后台概览。
func (a *App) handleMemberDashboard(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r)
	if user == nil {
		http.Redirect(w, r, "/admin/login", http.StatusFound)
		return
	}

	viewData := a.memberBaseData(r, "member", user)
	posts, _ := a.store.ListPostsByAuthor("post", user.ID)
	views := 0
	for _, post := range posts {
		views += int(post.Views)
	}
	viewData.Stats = Stats{Posts: len(posts), Views: views}
	viewData.AvatarRequests, _ = a.store.ListAvatarRequestsByUser(user.ID, 5)
	if r.URL.Query().Get("ok") == "1" {
		viewData.Flash = "操作已保存"
	}

	a.renderTemplate(w, "member_dashboard", viewData)
}

// handleMemberPosts 渲染“我的文章”列表。
func (a *App) handleMemberPosts(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r)
	if user == nil {
		http.Redirect(w, r, "/admin/login", http.StatusFound)
		return
	}
	posts, err := a.store.ListPostsByAuthor("post", user.ID)
	if err != nil {
		http.Error(w, "读取文章失败", http.StatusInternalServerError)
		return
	}

	viewData := a.memberBaseData(r, "member-posts", user)
	viewData.Posts = posts
	a.renderTemplate(w, "member_posts", viewData)
}

// handleMemberPostNew 新建访问者自己的文章。
func (a *App) handleMemberPostNew(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r)
	if user == nil {
		http.Redirect(w, r, "/admin/login", http.StatusFound)
		return
	}

	viewData := a.memberBaseData(r, "member-posts", user)
	switch r.Method {
	case http.MethodGet:
		a.renderTemplate(w, "member_post_form", viewData)
		return
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			viewData.Flash = "表单解析失败"
			a.renderTemplate(w, "member_post_form", viewData)
			return
		}
		post := buildPostFromForm(r, "post")
		post.AuthorID = user.ID
		if _, err := a.store.CreatePost(post); err != nil {
			viewData.Flash = "创建文章失败"
			viewData.Post = post
			a.renderTemplate(w, "member_post_form", viewData)
			return
		}
		http.Redirect(w, r, "/member/posts", http.StatusFound)
		return
	default:
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
}

// handleMemberPostEdit 编辑访问者自己的文章。
func (a *App) handleMemberPostEdit(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r)
	if user == nil {
		http.Redirect(w, r, "/admin/login", http.StatusFound)
		return
	}

	id := parseInt64(r.URL.Query().Get("id"))
	if id == 0 {
		http.NotFound(w, r)
		return
	}
	post, err := a.store.GetPostByIDAndAuthor(id, user.ID)
	if err != nil || post.Kind != "post" {
		http.NotFound(w, r)
		return
	}

	viewData := a.memberBaseData(r, "member-posts", user)
	switch r.Method {
	case http.MethodGet:
		viewData.Post = post
		a.renderTemplate(w, "member_post_form", viewData)
		return
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			viewData.Flash = "表单解析失败"
			viewData.Post = post
			a.renderTemplate(w, "member_post_form", viewData)
			return
		}

		updated := buildPostFromForm(r, "post")
		updated.ID = post.ID
		updated.Kind = "post"
		updated.Views = post.Views
		if updated.PublishedAt.IsZero() {
			updated.PublishedAt = post.PublishedAt
		}
		if err := a.store.UpdatePostByAuthor(updated, user.ID); err != nil {
			viewData.Flash = "更新文章失败"
			viewData.Post = updated
			a.renderTemplate(w, "member_post_form", viewData)
			return
		}
		http.Redirect(w, r, "/member/posts", http.StatusFound)
		return
	default:
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
}

// handleMemberPostDelete 删除访问者自己的文章。
func (a *App) handleMemberPostDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	user, _ := a.currentUser(r)
	if user == nil {
		http.Redirect(w, r, "/admin/login", http.StatusFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "表单解析失败", http.StatusBadRequest)
		return
	}
	id := parseInt64(r.FormValue("id"))
	if id == 0 {
		http.NotFound(w, r)
		return
	}
	_ = a.store.DeletePostByAuthor(id, user.ID)
	http.Redirect(w, r, "/member/posts", http.StatusFound)
}

// handleMemberPages 渲染“我的页面”列表。
func (a *App) handleMemberPages(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r)
	if user == nil {
		http.Redirect(w, r, "/admin/login", http.StatusFound)
		return
	}
	pages, err := a.store.ListPostsByAuthor("page", user.ID)
	if err != nil {
		http.Error(w, "读取页面失败", http.StatusInternalServerError)
		return
	}
	viewData := a.memberBaseData(r, "member-pages", user)
	viewData.Posts = pages
	if r.URL.Query().Get("ok") == "1" {
		viewData.Flash = "页面顺序已更新"
	}
	a.renderTemplate(w, "member_pages", viewData)
}

// handleMemberPageNew 新建访问者自己的页面。
// handleMemberPagesReorder 保存访问者自己的页面排序。
func (a *App) handleMemberPagesReorder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	user, _ := a.currentUser(r)
	if user == nil {
		http.Redirect(w, r, "/admin/login", http.StatusFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "表单解析失败", http.StatusBadRequest)
		return
	}
	ids := parseIDList(r.FormValue("ids"))
	if len(ids) == 0 {
		http.Error(w, "排序参数无效", http.StatusBadRequest)
		return
	}
	if err := a.store.ReorderPagesByAuthor(user.ID, ids); err != nil {
		http.Error(w, "保存排序失败", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/member/pages?ok=1", http.StatusFound)
}

func (a *App) handleMemberPageNew(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r)
	if user == nil {
		http.Redirect(w, r, "/admin/login", http.StatusFound)
		return
	}
	viewData := a.memberBaseData(r, "member-pages", user)
	switch r.Method {
	case http.MethodGet:
		a.renderTemplate(w, "member_page_form", viewData)
		return
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			viewData.Flash = "表单解析失败"
			a.renderTemplate(w, "member_page_form", viewData)
			return
		}
		page := buildPostFromForm(r, "page")
		page.AuthorID = user.ID
		if _, err := a.store.CreatePost(page); err != nil {
			viewData.Flash = "创建页面失败"
			viewData.Post = page
			a.renderTemplate(w, "member_page_form", viewData)
			return
		}
		http.Redirect(w, r, "/member/pages", http.StatusFound)
		return
	default:
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
}

// handleMemberPageEdit 编辑访问者自己的页面。
func (a *App) handleMemberPageEdit(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r)
	if user == nil {
		http.Redirect(w, r, "/admin/login", http.StatusFound)
		return
	}

	id := parseInt64(r.URL.Query().Get("id"))
	if id == 0 {
		http.NotFound(w, r)
		return
	}
	page, err := a.store.GetPostByIDAndAuthor(id, user.ID)
	if err != nil || page.Kind != "page" {
		http.NotFound(w, r)
		return
	}

	viewData := a.memberBaseData(r, "member-pages", user)
	switch r.Method {
	case http.MethodGet:
		viewData.Post = page
		a.renderTemplate(w, "member_page_form", viewData)
		return
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			viewData.Flash = "表单解析失败"
			viewData.Post = page
			a.renderTemplate(w, "member_page_form", viewData)
			return
		}

		updated := buildPostFromForm(r, "page")
		updated.ID = page.ID
		updated.Kind = "page"
		updated.Views = page.Views
		if updated.PublishedAt.IsZero() {
			updated.PublishedAt = page.PublishedAt
		}
		if err := a.store.UpdatePostByAuthor(updated, user.ID); err != nil {
			viewData.Flash = "更新页面失败"
			viewData.Post = updated
			a.renderTemplate(w, "member_page_form", viewData)
			return
		}
		http.Redirect(w, r, "/member/pages", http.StatusFound)
		return
	default:
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
}

// handleMemberPageDelete 删除访问者自己的页面。
func (a *App) handleMemberPageDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	user, _ := a.currentUser(r)
	if user == nil {
		http.Redirect(w, r, "/admin/login", http.StatusFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "表单解析失败", http.StatusBadRequest)
		return
	}
	id := parseInt64(r.FormValue("id"))
	if id == 0 {
		http.NotFound(w, r)
		return
	}
	_ = a.store.DeletePostByAuthor(id, user.ID)
	http.Redirect(w, r, "/member/pages", http.StatusFound)
}

// handleMemberComments 渲染“我的留言”列表。
func (a *App) handleMemberComments(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r)
	if user == nil {
		http.Redirect(w, r, "/admin/login", http.StatusFound)
		return
	}
	comments, err := a.store.ListCommentsByUser(user.ID)
	if err != nil {
		http.Error(w, "读取留言失败", http.StatusInternalServerError)
		return
	}
	viewData := a.memberBaseData(r, "member-comments", user)
	viewData.Comments = comments
	if r.URL.Query().Get("ok") == "1" {
		viewData.Flash = "留言已更新"
	}
	a.renderTemplate(w, "member_comments", viewData)
}

// handleMemberCommentEdit 修改访问者自己的留言。
func (a *App) handleMemberCommentEdit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	user, _ := a.currentUser(r)
	if user == nil {
		http.Redirect(w, r, "/admin/login", http.StatusFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "表单解析失败", http.StatusBadRequest)
		return
	}
	id := parseInt64(r.FormValue("id"))
	content := strings.TrimSpace(r.FormValue("content"))
	if id == 0 || content == "" {
		http.Error(w, "参数无效", http.StatusBadRequest)
		return
	}
	_ = a.store.UpdateCommentContentByUser(id, user.ID, content)
	http.Redirect(w, r, "/member/comments?ok=1", http.StatusFound)
}

// handleMemberCommentDelete 删除访问者自己的留言。
func (a *App) handleMemberCommentDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	user, _ := a.currentUser(r)
	if user == nil {
		http.Redirect(w, r, "/admin/login", http.StatusFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "表单解析失败", http.StatusBadRequest)
		return
	}
	id := parseInt64(r.FormValue("id"))
	if id == 0 {
		http.Error(w, "参数无效", http.StatusBadRequest)
		return
	}
	_ = a.store.DeleteCommentByUser(id, user.ID)
	http.Redirect(w, r, "/member/comments?ok=1", http.StatusFound)
}

// handleMemberAppearance 处理访问者后台外观。
func (a *App) handleMemberAppearance(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r)
	if user == nil {
		http.Redirect(w, r, "/admin/login", http.StatusFound)
		return
	}

	viewData := a.memberBaseData(r, "member-appearance", user)
	switch r.Method {
	case http.MethodGet:
		if r.URL.Query().Get("ok") == "1" {
			viewData.Flash = "外观设置已保存"
		}
		a.renderTemplate(w, "member_appearance", viewData)
		return
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			viewData.Flash = "表单解析失败"
			a.renderTemplate(w, "member_appearance", viewData)
			return
		}
		choice := sanitizeBackgroundChoice(r.FormValue("member_background"), allowedMemberBackgrounds, defaultAdminBackground)
		if err := a.store.SetUserSetting(user.ID, "member_background", choice); err != nil {
			viewData.Flash = "保存外观失败"
			a.renderTemplate(w, "member_appearance", viewData)
			return
		}
		http.Redirect(w, r, "/member/appearance?ok=1", http.StatusFound)
		return
	default:
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
}

// handleMemberProfile 处理访问者个人设置。
// 这里的资料修改会强制要求输入当前密码，避免登录态被窃后无门槛篡改账号信息。
func (a *App) handleMemberProfile(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r)
	if user == nil {
		http.Redirect(w, r, "/admin/login", http.StatusFound)
		return
	}

	viewData := a.memberBaseData(r, "member-profile", user)
	viewData.AvatarRequests, _ = a.store.ListAvatarRequestsByUser(user.ID, 20)
	if ok := strings.TrimSpace(r.URL.Query().Get("ok")); ok != "" {
		switch ok {
		case "display":
			viewData.Flash = "显示名已更新"
		case "password":
			viewData.Flash = "密码已更新"
		case "avatar":
			viewData.Flash = "头像已提交审核，请等待管理员处理"
		}
	}

	switch r.Method {
	case http.MethodGet:
		a.renderTemplate(w, "member_profile", viewData)
		return
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			viewData.Flash = "表单解析失败"
			a.renderTemplate(w, "member_profile", viewData)
			return
		}

		action := strings.TrimSpace(r.FormValue("action"))
		currentPassword := strings.TrimSpace(r.FormValue("current_password"))
		if err := a.store.VerifyUserPassword(user.ID, currentPassword); err != nil {
			viewData.Flash = "密码验证失败"
			a.renderTemplate(w, "member_profile", viewData)
			return
		}

		switch action {
		case "update_display_name":
			displayName := strings.TrimSpace(r.FormValue("display_name"))
			if displayName == "" {
				viewData.Flash = "显示名不能为空"
				a.renderTemplate(w, "member_profile", viewData)
				return
			}
			if err := a.store.UpdateUserDisplayName(user.ID, displayName); err != nil {
				viewData.Flash = "更新显示名失败"
				a.renderTemplate(w, "member_profile", viewData)
				return
			}
			http.Redirect(w, r, "/member/profile?ok=display", http.StatusFound)
			return
		case "update_password":
			newPassword := strings.TrimSpace(r.FormValue("new_password"))
			confirmPassword := strings.TrimSpace(r.FormValue("confirm_password"))
			if len([]rune(newPassword)) < 6 {
				viewData.Flash = "新密码至少 6 位"
				a.renderTemplate(w, "member_profile", viewData)
				return
			}
			if newPassword != confirmPassword {
				viewData.Flash = "两次输入的新密码不一致"
				a.renderTemplate(w, "member_profile", viewData)
				return
			}
			if err := a.store.UpdateUserPassword(user.ID, newPassword); err != nil {
				viewData.Flash = "更新密码失败"
				a.renderTemplate(w, "member_profile", viewData)
				return
			}
			http.Redirect(w, r, "/member/profile?ok=password", http.StatusFound)
			return
		default:
			viewData.Flash = "未知操作"
			a.renderTemplate(w, "member_profile", viewData)
			return
		}
	default:
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
}

// handleMemberAvatarUpload 接收访问者头像上传，并创建“待审核”申请。
func (a *App) handleMemberAvatarUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	user, _ := a.currentUser(r)
	if user == nil {
		http.Redirect(w, r, "/admin/login", http.StatusFound)
		return
	}

	if err := r.ParseMultipartForm(maxAvatarUploadBytes); err != nil {
		a.renderMemberProfileError(w, r, user, "上传失败：文件过大或表单无效")
		return
	}
	currentPassword := strings.TrimSpace(r.FormValue("current_password"))
	if err := a.store.VerifyUserPassword(user.ID, currentPassword); err != nil {
		a.renderMemberProfileError(w, r, user, "密码验证失败")
		return
	}

	file, header, err := r.FormFile("avatar_image")
	if err != nil {
		a.renderMemberProfileError(w, r, user, "上传失败：未选择图片")
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if _, ok := allowedBackgroundUploadExts[ext]; !ok {
		a.renderMemberProfileError(w, r, user, "上传失败：仅支持 jpg/png/gif/webp/svg")
		return
	}

	uploadDir := filepath.Join(a.staticDir, "uploads", "avatars")
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		a.renderMemberProfileError(w, r, user, "上传失败：创建目录失败")
		return
	}

	fileName := fmt.Sprintf("u%d-%d%s", user.ID, time.Now().UnixNano(), ext)
	savePath := filepath.Join(uploadDir, fileName)
	out, err := os.Create(savePath)
	if err != nil {
		a.renderMemberProfileError(w, r, user, "上传失败：写入文件失败")
		return
	}

	limited := io.LimitedReader{R: file, N: maxAvatarUploadBytes + 1}
	written, copyErr := io.Copy(out, &limited)
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(savePath)
		a.renderMemberProfileError(w, r, user, "上传失败：保存文件失败")
		return
	}
	if written > maxAvatarUploadBytes {
		_ = os.Remove(savePath)
		a.renderMemberProfileError(w, r, user, "上传失败：图片不能超过 4MB")
		return
	}

	avatarURL := fmt.Sprintf("/static/uploads/avatars/%s", fileName)
	if _, err := a.store.CreateAvatarRequest(user.ID, avatarURL); err != nil {
		_ = os.Remove(savePath)
		a.renderMemberProfileError(w, r, user, "上传失败：写入审核记录失败")
		return
	}

	http.Redirect(w, r, "/member/profile?ok=avatar", http.StatusFound)
}

// renderMemberProfileError 在个人设置页直接展示错误。
func (a *App) renderMemberProfileError(w http.ResponseWriter, r *http.Request, user *data.User, flash string) {
	viewData := a.memberBaseData(r, "member-profile", user)
	viewData.Flash = flash
	viewData.AvatarRequests, _ = a.store.ListAvatarRequestsByUser(user.ID, 20)
	a.renderTemplate(w, "member_profile", viewData)
}

func (a *App) handleMemberNotifications(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r)
	if user == nil {
		http.Redirect(w, r, "/admin/login", http.StatusFound)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	viewData := a.memberBaseData(r, "member-notifications", user)
	viewData.Notifications, _ = a.store.ListNotificationsByUser(user.ID, 100)
	if r.URL.Query().Get("ok") == "1" {
		viewData.Flash = "通知状态已更新"
	}
	a.renderTemplate(w, "member_notifications", viewData)
}

func (a *App) handleMemberNotificationsRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	user, _ := a.currentUser(r)
	if user == nil {
		http.Redirect(w, r, "/admin/login", http.StatusFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/member/notifications", http.StatusFound)
		return
	}
	action := strings.TrimSpace(r.FormValue("action"))
	switch action {
	case "all":
		_ = a.store.MarkAllNotificationsRead(user.ID)
	default:
		notificationID := parseInt64(r.FormValue("id"))
		if notificationID > 0 {
			_ = a.store.MarkNotificationRead(user.ID, notificationID)
		}
	}
	http.Redirect(w, r, "/member/notifications?ok=1", http.StatusFound)
}

func (a *App) handleMemberSessions(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r)
	if user == nil {
		http.Redirect(w, r, "/admin/login", http.StatusFound)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	viewData := a.memberBaseData(r, "member-sessions", user)
	viewData.Sessions, _ = a.store.ListSessionsByUser(user.ID)
	if r.URL.Query().Get("ok") == "1" {
		viewData.Flash = "会话已下线"
	}
	a.renderTemplate(w, "member_sessions", viewData)
}

func (a *App) handleMemberSessionRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	user, _ := a.currentUser(r)
	if user == nil {
		http.Redirect(w, r, "/admin/login", http.StatusFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/member/sessions", http.StatusFound)
		return
	}
	sessionID := strings.TrimSpace(r.FormValue("session_id"))
	if sessionID != "" {
		_ = a.store.DeleteSessionByUser(sessionID, user.ID)
	}
	http.Redirect(w, r, "/member/sessions?ok=1", http.StatusFound)
}
