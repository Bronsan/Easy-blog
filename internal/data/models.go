package data

import "time"

const (
	// RoleOwner 站长：拥有所有后台权限，可管理用户与权限。
	RoleOwner = "owner"
	// RoleMaintainer 维护：拥有内容管理权限，但不能管理用户角色。
	RoleMaintainer = "maintainer"
	// RoleVisitor 访问者：进入访问者后台，仅可管理自己的内容与资料。
	RoleVisitor = "visitor"
)

// User 表示一个系统用户（前台/后台共用）。
type User struct {
	ID          int64
	Username    string
	DisplayName string
	// AvatarURL 为当前可用头像地址。
	// 仅当头像申请审核通过后，系统才会更新该字段。
	AvatarURL string

	Role string

	Email         string
	EmailVerified bool
	IsBanned      bool
	BannedReason  string
	BannedAt      time.Time

	LastLoginIP string
	LastLoginAt time.Time
	CreatedAt   time.Time
}

// Post 表示文章或页面。
type Post struct {
	ID          int64
	Title       string
	Slug        string
	Summary     string
	Content     string
	Kind        string
	CoverURL    string
	PublishedAt time.Time
	UpdatedAt   time.Time
	AuthorID    int64
	// SortOrder 用于页面管理中的自由排序（值越小越靠前）。
	// 文章模块仍按发布时间等规则展示，不依赖该字段。
	SortOrder int64
	Views     int64
	IsPublic  bool
}

// Comment 表示留言（包含留言板与未来论坛评论场景）。
type Comment struct {
	ID     int64
	PostID int64
	// UserID 为留言归属用户，0 表示游客留言。
	UserID int64

	Author      string
	Content     string
	IP          string
	IsAnonymous bool
	IsHidden    bool
	Likes       int64
	Dislikes    int64
	CreatedAt   time.Time

	// AvatarURL 为留言关联用户头像（已审核通过头像）。
	AvatarURL string
	// PostTitle / PostSlug 主要用于“我的留言”模块显示来源。
	PostTitle string
	PostSlug  string
}

// Setting 表示全站设置。
type Setting struct {
	Key   string
	Value string
}

// AuditLog 表示后台用户管理审计日志。
type AuditLog struct {
	ID int64

	ActorUserID   int64
	ActorUsername string

	TargetUserID   int64
	TargetUsername string

	Action string
	Detail string

	ActorIP   string
	CreatedAt time.Time
}

const (
	// AvatarStatusPending 表示头像申请待审核。
	AvatarStatusPending = "pending"
	// AvatarStatusApproved 表示头像申请已通过。
	AvatarStatusApproved = "approved"
	// AvatarStatusRejected 表示头像申请已驳回。
	AvatarStatusRejected = "rejected"
)

// AvatarRequest 表示一次用户头像审核申请记录。
type AvatarRequest struct {
	ID int64

	UserID      int64
	Username    string
	DisplayName string

	AvatarURL string
	Status    string

	ReviewNote string

	CreatedAt    time.Time
	ReviewedAt   time.Time
	ReviewerID   int64
	ReviewerName string
}

// SessionInfo 用于“当前用户会话管理”页面展示。
type SessionInfo struct {
	ID        string
	UserID    int64
	IP        string
	UserAgent string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// AdminSessionInfo 用于站长后台展示所有用户的会话记录。
type AdminSessionInfo struct {
	ID          string
	UserID      int64
	Username    string
	DisplayName string
	IP          string
	UserAgent   string
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

const (
	CommentReportStatusPending  = "pending"
	CommentReportStatusApproved = "approved"
	CommentReportStatusRejected = "rejected"
)

// CommentReport 表示一条留言举报记录。
type CommentReport struct {
	ID int64

	CommentID      int64
	CommentContent string

	ReporterUserID      int64
	ReporterUsername    string
	ReporterDisplayName string

	Reason string
	Status string

	ReviewNote string
	ReviewerID int64

	CreatedAt  time.Time
	ReviewedAt time.Time
}

// Notification 表示站内消息通知。
type Notification struct {
	ID      int64
	UserID  int64
	Title   string
	Content string
	Kind    string
	IsRead  bool

	CreatedAt time.Time
}

// PostReport 表示一条文章举报记录。
type PostReport struct {
	ID int64

	PostID    int64
	PostTitle string
	PostSlug  string

	ReporterUserID      int64
	ReporterUsername    string
	ReporterDisplayName string

	Reason string
	Status string

	ReviewNote string
	ReviewerID int64

	CreatedAt  time.Time
	ReviewedAt time.Time
}
