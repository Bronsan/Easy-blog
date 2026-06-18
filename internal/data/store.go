package data

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

const (
	// defaultUserAvatar 是默认用户头像地址。
	defaultUserAvatar = "/static/img/avatar.svg"

	// Argon2id 默认参数。
	argon2MemoryKB   uint32 = 64 * 1024
	argon2Iterations uint32 = 3
	argon2Parallel   uint8  = 2
	argon2SaltLength        = 16
	argon2KeyLength  uint32 = 32
)

// Store 封装数据库访问与数据操作。
type Store struct {
	db                 *sql.DB
	slowQueryThreshold time.Duration
}

// Open 打开数据库并初始化存储。
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("store error: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store error: %w", err)
	}

	s := &Store{db: db, slowQueryThreshold: 200 * time.Millisecond}
	if v := strings.TrimSpace(os.Getenv("BLOG_SLOW_QUERY_MS")); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			s.slowQueryThreshold = time.Duration(ms) * time.Millisecond
		}
	}
	if err := s.initPragma(); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.ensureSchema(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭数据库连接。
func (s *Store) Close() error {
	return s.db.Close()
}

// initPragma 初始化 SQLite 的 PRAGMA 配置。
func (s *Store) initPragma() error {
	stmts := []string{
		"PRAGMA foreign_keys = ON;",
		"PRAGMA journal_mode = WAL;",
		"PRAGMA synchronous = NORMAL;",
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("store error: %w", err)
		}
	}
	return nil
}

// ensureSchema 创建表和索引，并应用迁移。
func (s *Store) ensureSchema() error {
	schema := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT NOT NULL UNIQUE,
			display_name TEXT NOT NULL,
			password_hash TEXT NOT NULL,
			email TEXT NOT NULL DEFAULT '',
			email_verified INTEGER NOT NULL DEFAULT 0,
			avatar_url TEXT NOT NULL DEFAULT '/static/img/avatar.svg',
			role TEXT NOT NULL DEFAULT 'visitor',
			is_banned INTEGER NOT NULL DEFAULT 0,
			banned_reason TEXT NOT NULL DEFAULT '',
			banned_at INTEGER NOT NULL DEFAULT 0,
			last_login_ip TEXT NOT NULL DEFAULT '',
			last_login_at INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			user_id INTEGER NOT NULL,
			ip TEXT NOT NULL DEFAULT '',
			user_agent TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT 0,
			expires_at INTEGER NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS posts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			title TEXT NOT NULL,
			slug TEXT NOT NULL UNIQUE,
			summary TEXT NOT NULL,
			content TEXT NOT NULL,
			kind TEXT NOT NULL,
			cover_url TEXT NOT NULL,
			published_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			author_id INTEGER NOT NULL,
			sort_order INTEGER NOT NULL DEFAULT 0,
			is_public INTEGER NOT NULL DEFAULT 1,
			views INTEGER NOT NULL DEFAULT 0
		);`,
		`CREATE TABLE IF NOT EXISTS comments (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			post_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL DEFAULT 0,
			author TEXT NOT NULL,
			content TEXT NOT NULL,
			ip TEXT NOT NULL DEFAULT '',
			is_anonymous INTEGER NOT NULL DEFAULT 0,
			is_hidden INTEGER NOT NULL DEFAULT 0,
			likes INTEGER NOT NULL DEFAULT 0,
			dislikes INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			FOREIGN KEY(post_id) REFERENCES posts(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS avatar_requests (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			avatar_url TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			review_note TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			reviewed_at INTEGER NOT NULL DEFAULT 0,
			reviewer_user_id INTEGER NOT NULL DEFAULT 0,
			FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS user_settings (
			user_id INTEGER NOT NULL,
			key TEXT NOT NULL,
			value TEXT NOT NULL,
			PRIMARY KEY (user_id, key),
			FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS audit_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			actor_user_id INTEGER NOT NULL,
			target_user_id INTEGER NOT NULL,
			action TEXT NOT NULL,
			detail TEXT NOT NULL,
			actor_ip TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_posts_kind_pub ON posts (kind, published_at DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions (user_id);`,
		`CREATE INDEX IF NOT EXISTS idx_comments_post ON comments (post_id, created_at DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_avatar_requests_user ON avatar_requests (user_id, created_at DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_avatar_requests_status ON avatar_requests (status, created_at DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_audit_logs_created ON audit_logs (created_at DESC);`,
	}

	for _, stmt := range schema {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("store error: %w", err)
		}
	}

	if err := s.ensureUserColumns(); err != nil {
		return err
	}
	if err := s.ensureSessionColumns(); err != nil {
		return err
	}
	if err := s.ensureCommentColumns(); err != nil {
		return err
	}
	if err := s.ensurePostColumns(); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_posts_kind_author_order ON posts (kind, author_id, sort_order ASC);`); err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_comments_user ON comments (user_id, created_at DESC);`); err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	if err := s.ensureOwnerExists(); err != nil {
		return err
	}
	if err := s.applyMigrations(); err != nil {
		return err
	}
	return nil
}

// ensureUserColumns 确保 users 表字段齐全。
func (s *Store) ensureUserColumns() error {
	type patch struct {
		name string
		sql  string
	}
	patches := []patch{
		{name: "role", sql: `ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'visitor';`},
		{name: "avatar_url", sql: `ALTER TABLE users ADD COLUMN avatar_url TEXT NOT NULL DEFAULT '/static/img/avatar.svg';`},
		{name: "email", sql: `ALTER TABLE users ADD COLUMN email TEXT NOT NULL DEFAULT '';`},
		{name: "email_verified", sql: `ALTER TABLE users ADD COLUMN email_verified INTEGER NOT NULL DEFAULT 0;`},
		{name: "is_banned", sql: `ALTER TABLE users ADD COLUMN is_banned INTEGER NOT NULL DEFAULT 0;`},
		{name: "banned_reason", sql: `ALTER TABLE users ADD COLUMN banned_reason TEXT NOT NULL DEFAULT '';`},
		{name: "banned_at", sql: `ALTER TABLE users ADD COLUMN banned_at INTEGER NOT NULL DEFAULT 0;`},
		{name: "last_login_ip", sql: `ALTER TABLE users ADD COLUMN last_login_ip TEXT NOT NULL DEFAULT '';`},
		{name: "last_login_at", sql: `ALTER TABLE users ADD COLUMN last_login_at INTEGER NOT NULL DEFAULT 0;`},
	}

	for _, p := range patches {
		exists, err := s.columnExists("users", p.name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := s.db.Exec(p.sql); err != nil {
			return fmt.Errorf("store error on %s: %w", p.name, err)
		}
	}
	return nil
}

// ensureSessionColumns 确保 sessions 表字段齐全。
func (s *Store) ensureSessionColumns() error {
	type patch struct {
		name string
		sql  string
	}
	patches := []patch{
		{name: "ip", sql: `ALTER TABLE sessions ADD COLUMN ip TEXT NOT NULL DEFAULT '';`},
		{name: "user_agent", sql: `ALTER TABLE sessions ADD COLUMN user_agent TEXT NOT NULL DEFAULT '';`},
		{name: "created_at", sql: `ALTER TABLE sessions ADD COLUMN created_at INTEGER NOT NULL DEFAULT 0;`},
	}
	for _, p := range patches {
		exists, err := s.columnExists("sessions", p.name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := s.db.Exec(p.sql); err != nil {
			return fmt.Errorf("store error on %s: %w", p.name, err)
		}
	}
	return nil
}

// ensureCommentColumns 确保 comments 表字段齐全。
func (s *Store) ensureCommentColumns() error {
	type patch struct {
		name string
		sql  string
	}
	patches := []patch{
		{name: "user_id", sql: `ALTER TABLE comments ADD COLUMN user_id INTEGER NOT NULL DEFAULT 0;`},
		{name: "ip", sql: `ALTER TABLE comments ADD COLUMN ip TEXT NOT NULL DEFAULT '';`},
		{name: "is_hidden", sql: `ALTER TABLE comments ADD COLUMN is_hidden INTEGER NOT NULL DEFAULT 0;`},
		{name: "likes", sql: `ALTER TABLE comments ADD COLUMN likes INTEGER NOT NULL DEFAULT 0;`},
		{name: "dislikes", sql: `ALTER TABLE comments ADD COLUMN dislikes INTEGER NOT NULL DEFAULT 0;`},
	}

	for _, p := range patches {
		exists, err := s.columnExists("comments", p.name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := s.db.Exec(p.sql); err != nil {
			return fmt.Errorf("store error on %s: %w", p.name, err)
		}
	}
	return nil
}

// ensurePostColumns 确保 posts 表字段齐全。
// sort_order 用于页面排序字段。
func (s *Store) ensurePostColumns() error {
	exists, err := s.columnExists("posts", "sort_order")
	if err != nil {
		return err
	}
	if !exists {
		if _, err := s.db.Exec(`ALTER TABLE posts ADD COLUMN sort_order INTEGER NOT NULL DEFAULT 0;`); err != nil {
			return fmt.Errorf("store error: %w", err)
		}
	}
	if _, err := s.db.Exec(`UPDATE posts SET sort_order = id WHERE sort_order <= 0;`); err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	return nil
}

// ensureOwnerExists 确保至少存在一个站长账号。
func (s *Store) ensureOwnerExists() error {
	var ownerCount int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM users WHERE role = ?;`, RoleOwner).Scan(&ownerCount); err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	if ownerCount > 0 {
		return nil
	}

	_, err := s.db.Exec(`UPDATE users SET role = ? WHERE id = (SELECT id FROM users ORDER BY id ASC LIMIT 1);`, RoleOwner)
	if err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	return nil
}

// columnExists 判断指定表字段是否存在。
func (s *Store) columnExists(table, column string) (bool, error) {
	rows, err := s.db.Query(fmt.Sprintf("PRAGMA table_info(%s);", table))
	if err != nil {
		return false, fmt.Errorf("store error: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid       int
			name      string
			colType   string
			notNull   int
			dfltValue any
			pk        int
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			return false, fmt.Errorf("store error: %w", err)
		}
		if strings.EqualFold(name, column) {
			return true, nil
		}
	}
	return false, nil
}

// EnsureAdmin 在系统首次启动时确保默认管理员存在。
func (s *Store) EnsureAdmin(username, password, displayName string) (bool, error) {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM users;`).Scan(&count); err != nil {
		return false, fmt.Errorf("store error: %w", err)
	}
	if count > 0 {
		return false, nil
	}
	if _, err := s.CreateUserWithRole(username, password, displayName, RoleOwner); err != nil {
		return false, err
	}
	return true, nil
}

// SeedDefaults 插入默认设置、默认页面和示例文章。
func (s *Store) SeedDefaults() error {
	if err := s.EnsureSetting("notice", "记录和分享自己的学习过程"); err != nil {
		return err
	}
	if err := s.EnsureSetting("daily_quote", "朝闻道，夕死可矣。"); err != nil {
		return err
	}
	if err := s.EnsureSetting("avatar_url", defaultUserAvatar); err != nil {
		return err
	}
	if err := s.EnsureSetting("site_background", "url('/static/img/paper-bg.svg') center top / cover no-repeat fixed"); err != nil {
		return err
	}
	if err := s.EnsureSetting("admin_background", "linear-gradient(180deg, #f6f6f2, #eceff3)"); err != nil {
		return err
	}

	if err := s.ensurePage("about", "关于", "这里是关于页面，你可以在后台继续编辑。\n\n- 支持 Markdown\n- 支持多页面"); err != nil {
		return err
	}
	if err := s.ensurePage("say", "说说", "这里是说说页面，记录你的灵感碎片。\n\n- 今天也要认真写代码\n- 记得常常抬头看天空"); err != nil {
		return err
	}
	if err := s.ensurePage("board", "留言板", "欢迎留言。\n\n你可以匿名，也可以署名。"); err != nil {
		return err
	}

	var count int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM posts WHERE kind='post';`).Scan(&count); err != nil {
		return fmt.Errorf("查询示例文章数量失败: %w", err)
	}
	if count > 0 {
		return nil
	}

	now := time.Now().Unix()
	samples := []Post{
		{
			Title:    "Hello World",
			Slug:     "hello-world",
			Summary:  "第一篇示例文章。",
			Content:  "这是示例内容，后续可以在后台自由替换。",
			Kind:     "post",
			CoverURL: "/static/img/cover-1.svg",
		},
		{
			Title:    "本地部署笔记",
			Slug:     "local-deploy-notes",
			Summary:  "记录一次从本地运行到上线的过程。",
			Content:  "示例内容：你可以写数据库迁移、反向代理、备份策略等。",
			Kind:     "post",
			CoverURL: "/static/img/cover-2.svg",
		},
	}

	for idx, post := range samples {
		if _, err := s.db.Exec(
			`INSERT INTO posts (title, slug, summary, content, kind, cover_url, published_at, updated_at, author_id, sort_order, is_public, views)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 0);`,
			post.Title, post.Slug, post.Summary, post.Content, post.Kind, post.CoverURL, now, now, 1, idx+1,
		); err != nil {
			return fmt.Errorf("写入示例文章失败: %w", err)
		}
	}
	return nil
}
func (s *Store) ensurePage(slug, title, content string) error {
	now := time.Now().Unix()
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO posts (title, slug, summary, content, kind, cover_url, published_at, updated_at, author_id, sort_order, is_public, views)
		 VALUES (?, ?, ?, ?, 'page', '', ?, ?, 1, (SELECT COALESCE(MAX(sort_order), 0) + 1 FROM posts WHERE kind = 'page' AND author_id = 1), 1, 0);`,
		title, slug, title, content, now, now,
	)
	if err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	return nil
}

// EnsureSetting 确保设置项存在。
func (s *Store) EnsureSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO settings (key, value) VALUES (?, ?);`, key, value)
	if err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	return nil
}

// CreateUser 创建用户。
func (s *Store) CreateUser(username, password, displayName string) error {
	_, err := s.CreateUserWithRole(username, password, displayName, RoleVisitor)
	return err
}

// CreateUserWithRole 创建用户并写入规范化后的角色。
func (s *Store) CreateUserWithRole(username, password, displayName, role string) (int64, error) {
	username = strings.TrimSpace(username)
	displayName = strings.TrimSpace(displayName)
	password = strings.TrimSpace(password)
	if username == "" || displayName == "" || password == "" {
		return 0, errors.New("username, display name, and password are required")
	}

	hash, err := hashPasswordArgon2id(password)
	if err != nil {
		return 0, fmt.Errorf("hash password: %w", err)
	}
	role = normalizeRole(role)

	res, err := s.db.Exec(
		`INSERT INTO users (username, display_name, password_hash, avatar_url, role, last_login_ip, last_login_at, created_at)
		 VALUES (?, ?, ?, ?, ?, '', 0, ?);`,
		username, displayName, hash, defaultUserAvatar, role, time.Now().Unix(),
	)
	if err != nil {
		return 0, fmt.Errorf("create user: %w", err)
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// Authenticate 校验凭据并返回匹配的用户。
func (s *Store) Authenticate(username, password string) (*User, error) {
	username = strings.TrimSpace(username)
	password = strings.TrimSpace(password)
	if username == "" || password == "" {
		return nil, errors.New("invalid username or password")
	}

	var (
		id            int64
		displayName   string
		avatarURL     string
		role          string
		email         string
		emailVerified int
		isBanned      int
		bannedReason  string
		bannedAt      int64
		lastLoginIP   string
		lastLoginAt   int64
		hash          string
		createdAt     int64
	)
	err := s.db.QueryRow(
		`SELECT id, display_name, avatar_url, role, email, email_verified, is_banned, banned_reason, banned_at, last_login_ip, last_login_at, password_hash, created_at
		 FROM users WHERE username = ?;`, username,
	).Scan(&id, &displayName, &avatarURL, &role, &email, &emailVerified, &isBanned, &bannedReason, &bannedAt, &lastLoginIP, &lastLoginAt, &hash, &createdAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("invalid username or password")
		}
		return nil, fmt.Errorf("load user for authentication: %w", err)
	}

	ok, needUpgrade, verifyErr := verifyPassword(hash, password)
	if verifyErr != nil || !ok {
		return nil, errors.New("invalid username or password")
	}
	if needUpgrade {
		if newHash, hashErr := hashPasswordArgon2id(password); hashErr == nil {
			_, _ = s.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?;`, newHash, id)
		}
	}
	if isBanned == 1 {
		if strings.TrimSpace(bannedReason) == "" {
			bannedReason = "account is banned"
		}
		return nil, errors.New(bannedReason)
	}

	return &User{
		ID:            id,
		Username:      username,
		DisplayName:   displayName,
		AvatarURL:     avatarURL,
		Role:          normalizeRole(role),
		Email:         email,
		EmailVerified: emailVerified == 1,
		IsBanned:      isBanned == 1,
		BannedReason:  bannedReason,
		BannedAt:      unixToTime(bannedAt),
		LastLoginIP:   lastLoginIP,
		LastLoginAt:   unixToTime(lastLoginAt),
		CreatedAt:     unixToTime(createdAt),
	}, nil
}

// GetUserByID 根据 ID 获取用户。
func (s *Store) GetUserByID(id int64) (*User, error) {
	var user User
	var createdAt, lastLoginAt, bannedAt int64
	var emailVerified, isBanned int
	err := s.db.QueryRow(
		`SELECT id, username, display_name, avatar_url, role, email, email_verified, is_banned, banned_reason, banned_at, last_login_ip, last_login_at, created_at
		 FROM users WHERE id = ?;`,
		id,
	).Scan(&user.ID, &user.Username, &user.DisplayName, &user.AvatarURL, &user.Role, &user.Email, &emailVerified, &isBanned, &user.BannedReason, &bannedAt, &user.LastLoginIP, &lastLoginAt, &createdAt)
	if err != nil {
		return nil, err
	}
	user.Role = normalizeRole(user.Role)
	user.EmailVerified = emailVerified == 1
	user.IsBanned = isBanned == 1
	user.BannedAt = unixToTime(bannedAt)
	user.LastLoginAt = unixToTime(lastLoginAt)
	user.CreatedAt = unixToTime(createdAt)
	return &user, nil
}

// ListUsers 按 ID 顺序返回所有用户。
func (s *Store) ListUsers() ([]User, error) {
	defer s.logSlow("ListUsers", time.Now())

	rows, err := s.db.Query(`SELECT id, username, display_name, avatar_url, role, email, email_verified, is_banned, banned_reason, banned_at, last_login_ip, last_login_at, created_at FROM users ORDER BY id ASC;`)
	if err != nil {
		return nil, fmt.Errorf("load users: %w", err)
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		var createdAt, lastLoginAt, bannedAt int64
		var emailVerified, isBanned int
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.AvatarURL, &u.Role, &u.Email, &emailVerified, &isBanned, &u.BannedReason, &bannedAt, &u.LastLoginIP, &lastLoginAt, &createdAt); err != nil {
			return nil, fmt.Errorf("scan users: %w", err)
		}
		u.Role = normalizeRole(u.Role)
		u.EmailVerified = emailVerified == 1
		u.IsBanned = isBanned == 1
		u.BannedAt = unixToTime(bannedAt)
		u.LastLoginAt = unixToTime(lastLoginAt)
		u.CreatedAt = unixToTime(createdAt)
		users = append(users, u)
	}
	return users, nil
}

// VerifyUserPassword 校验用户密码。
func (s *Store) VerifyUserPassword(userID int64, password string) error {
	password = strings.TrimSpace(password)
	if userID == 0 || password == "" {
		return errors.New("operation failed")
	}

	var hash string
	err := s.db.QueryRow(`SELECT password_hash FROM users WHERE id = ?;`, userID).Scan(&hash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("operation failed")
		}
		return fmt.Errorf("store error: %w", err)
	}

	ok, needUpgrade, verifyErr := verifyPassword(hash, password)
	if verifyErr != nil || !ok {
		return errors.New("operation failed")
	}
	if needUpgrade {
		if newHash, hashErr := hashPasswordArgon2id(password); hashErr == nil {
			_, _ = s.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?;`, newHash, userID)
		}
	}
	return nil
}

// UpdateUserProfile 更新用户名和显示名。
func (s *Store) UpdateUserProfile(id int64, username, displayName string) error {
	username = strings.TrimSpace(username)
	displayName = strings.TrimSpace(displayName)
	if id == 0 || username == "" || displayName == "" {
		return errors.New("operation failed")
	}
	_, err := s.db.Exec(`UPDATE users SET username = ?, display_name = ? WHERE id = ?;`, username, displayName, id)
	if err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	return nil
}

// UpdateUserDisplayName 更新显示名。
func (s *Store) UpdateUserDisplayName(userID int64, displayName string) error {
	displayName = strings.TrimSpace(displayName)
	if userID == 0 || displayName == "" {
		return errors.New("operation failed")
	}
	_, err := s.db.Exec(`UPDATE users SET display_name = ? WHERE id = ?;`, displayName, userID)
	if err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	return nil
}

// UpdateUserRole 更新用户角色。
func (s *Store) UpdateUserRole(id int64, role string) error {
	if id == 0 {
		return errors.New("operation failed")
	}
	role = normalizeRole(role)

	var oldRole string
	if err := s.db.QueryRow(`SELECT role FROM users WHERE id = ?;`, id).Scan(&oldRole); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("operation failed")
		}
		return fmt.Errorf("store error: %w", err)
	}
	oldRole = normalizeRole(oldRole)

	if oldRole == RoleOwner && role != RoleOwner {
		var ownerCount int
		if err := s.db.QueryRow(`SELECT COUNT(1) FROM users WHERE role = ?;`, RoleOwner).Scan(&ownerCount); err != nil {
			return fmt.Errorf("store error: %w", err)
		}
		if ownerCount <= 1 {
			return errors.New("operation failed")
		}
	}

	_, err := s.db.Exec(`UPDATE users SET role = ? WHERE id = ?;`, role, id)
	if err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	return nil
}

// UpdateUserPassword 更新用户密码。
func (s *Store) UpdateUserPassword(id int64, newPassword string) error {
	newPassword = strings.TrimSpace(newPassword)
	if id == 0 || newPassword == "" {
		return errors.New("operation failed")
	}
	hash, err := hashPasswordArgon2id(newPassword)
	if err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	_, err = s.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?;`, hash, id)
	if err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	return nil
}

// DeleteUser 删除用户。
func (s *Store) DeleteUser(id int64) error {
	if id == 0 {
		return errors.New("operation failed")
	}

	var role string
	if err := s.db.QueryRow(`SELECT role FROM users WHERE id = ?;`, id).Scan(&role); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("operation failed")
		}
		return fmt.Errorf("store error: %w", err)
	}
	role = normalizeRole(role)
	if role == RoleOwner {
		var ownerCount int
		if err := s.db.QueryRow(`SELECT COUNT(1) FROM users WHERE role = ?;`, RoleOwner).Scan(&ownerCount); err != nil {
			return fmt.Errorf("store error: %w", err)
		}
		if ownerCount <= 1 {
			return errors.New("operation failed")
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM sessions WHERE user_id = ?;`, id); err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM user_settings WHERE user_id = ?;`, id); err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM avatar_requests WHERE user_id = ? OR reviewer_user_id = ?;`, id, id); err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM audit_logs WHERE actor_user_id = ? OR target_user_id = ?;`, id, id); err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM users WHERE id = ?;`, id); err != nil {
		return fmt.Errorf("store error: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	return nil
}

// UpdateLastLogin 更新最近登录信息。
func (s *Store) UpdateLastLogin(id int64, ip string) error {
	if id == 0 {
		return errors.New("operation failed")
	}
	ip = strings.TrimSpace(ip)
	if len(ip) > 128 {
		ip = ip[:128]
	}
	_, err := s.db.Exec(`UPDATE users SET last_login_ip = ?, last_login_at = ? WHERE id = ?;`, ip, time.Now().Unix(), id)
	if err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	return nil
}

// CreateAuditLog 写入审计日志。
func (s *Store) CreateAuditLog(actorUserID, targetUserID int64, action, detail, actorIP string) error {
	if actorUserID == 0 || targetUserID == 0 {
		return errors.New("operation failed")
	}
	action = strings.TrimSpace(action)
	detail = strings.TrimSpace(detail)
	actorIP = strings.TrimSpace(actorIP)
	if action == "" {
		return errors.New("operation failed")
	}
	if len(detail) > 1000 {
		detail = detail[:1000]
	}
	if len(actorIP) > 128 {
		actorIP = actorIP[:128]
	}
	_, err := s.db.Exec(
		`INSERT INTO audit_logs (actor_user_id, target_user_id, action, detail, actor_ip, created_at)
		 VALUES (?, ?, ?, ?, ?, ?);`,
		actorUserID, targetUserID, action, detail, actorIP, time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	return nil
}

// ListAuditLogs 返回最近的审计日志记录。
func (s *Store) ListAuditLogs(limit int) ([]AuditLog, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT l.id, l.actor_user_id, COALESCE(a.username, '(deleted user)'), l.target_user_id, COALESCE(t.username, '(deleted user)'),
				l.action, l.detail, l.actor_ip, l.created_at
		 FROM audit_logs l
		 LEFT JOIN users a ON a.id = l.actor_user_id
		 LEFT JOIN users t ON t.id = l.target_user_id
		 ORDER BY l.created_at DESC
		 LIMIT ?;`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("store error: %w", err)
	}
	defer rows.Close()

	var logs []AuditLog
	for rows.Next() {
		var item AuditLog
		var createdAt int64
		if err := rows.Scan(&item.ID, &item.ActorUserID, &item.ActorUsername, &item.TargetUserID, &item.TargetUsername, &item.Action, &item.Detail, &item.ActorIP, &createdAt); err != nil {
			return nil, fmt.Errorf("store error: %w", err)
		}
		item.CreatedAt = unixToTime(createdAt)
		logs = append(logs, item)
	}
	return logs, nil
}

// CreateSession 创建会话。
func (s *Store) CreateSession(userID int64, ttl time.Duration) (string, int64, error) {
	return s.CreateSessionWithMeta(userID, "", "", ttl)
}

func (s *Store) CreateSessionWithMeta(userID int64, ip, userAgent string, ttl time.Duration) (string, int64, error) {
	ip = strings.TrimSpace(ip)
	userAgent = strings.TrimSpace(userAgent)
	if len(ip) > 128 {
		ip = ip[:128]
	}
	if len(userAgent) > 512 {
		userAgent = userAgent[:512]
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", 0, fmt.Errorf("store error: %w", err)
	}
	sessionID := base64.RawURLEncoding.EncodeToString(raw)
	now := time.Now()
	expiresAt := now.Add(ttl).Unix()
	_, err := s.db.Exec(
		`INSERT INTO sessions (id, user_id, ip, user_agent, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?);`,
		sessionID, userID, ip, userAgent, now.Unix(), expiresAt,
	)
	if err != nil {
		return "", 0, fmt.Errorf("store error: %w", err)
	}
	return sessionID, expiresAt, nil
}

// GetSession 根据会话 ID 读取会话。
func (s *Store) GetSession(id string) (int64, int64, error) {
	var userID, expiresAt int64
	err := s.db.QueryRow(`SELECT user_id, expires_at FROM sessions WHERE id = ?;`, id).Scan(&userID, &expiresAt)
	if err != nil {
		return 0, 0, err
	}
	return userID, expiresAt, nil
}

// DeleteSession 删除会话。
func (s *Store) DeleteSession(id string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE id = ?;`, id)
	return err
}

// CleanupSessions 清理过期会话。
func (s *Store) CleanupSessions() error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at < ?;`, time.Now().Unix())
	return err
}

// CreatePost 创建文章或页面。
func (s *Store) CreatePost(post *Post) (int64, error) {
	if post == nil {
		return 0, errors.New("operation failed")
	}
	if post.Slug == "" {
		post.Slug = slugify(post.Title)
	}
	if post.Slug == "" {
		post.Slug = fmt.Sprintf("post-%d", time.Now().Unix())
	}
	slug, err := s.ensureUniqueSlug(post.Slug)
	if err != nil {
		return 0, err
	}
	post.Slug = slug

	now := time.Now()
	if post.PublishedAt.IsZero() {
		post.PublishedAt = now
	}
	post.UpdatedAt = now
	if post.AuthorID == 0 {
		post.AuthorID = 1
	}
	if post.SortOrder <= 0 {
		nextOrder, orderErr := s.nextSortOrder(post.Kind, post.AuthorID)
		if orderErr != nil {
			return 0, orderErr
		}
		post.SortOrder = nextOrder
	}

	res, err := s.db.Exec(
		`INSERT INTO posts (title, slug, summary, content, kind, cover_url, published_at, updated_at, author_id, sort_order, is_public, views)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);`,
		post.Title, post.Slug, post.Summary, post.Content, post.Kind, post.CoverURL,
		post.PublishedAt.Unix(), post.UpdatedAt.Unix(), post.AuthorID, post.SortOrder, boolToInt(post.IsPublic), post.Views,
	)
	if err != nil {
		return 0, fmt.Errorf("store error: %w", err)
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// UpdatePost 更新文章或页面。
func (s *Store) UpdatePost(post *Post) error {
	if post == nil {
		return errors.New("operation failed")
	}
	if post.Slug == "" {
		post.Slug = slugify(post.Title)
	}
	if post.Slug == "" {
		post.Slug = fmt.Sprintf("post-%d", post.ID)
	}
	slug, err := s.ensureUniqueSlugForID(post.Slug, post.ID)
	if err != nil {
		return err
	}
	post.Slug = slug
	post.UpdatedAt = time.Now()

	_, err = s.db.Exec(
		`UPDATE posts
		 SET title = ?, slug = ?, summary = ?, content = ?, kind = ?, cover_url = ?, published_at = ?, updated_at = ?, is_public = ?
		 WHERE id = ?;`,
		post.Title, post.Slug, post.Summary, post.Content, post.Kind, post.CoverURL,
		post.PublishedAt.Unix(), post.UpdatedAt.Unix(), boolToInt(post.IsPublic), post.ID,
	)
	if err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	return nil
}

// UpdatePostByAuthor 由作者本人更新文章或页面。
func (s *Store) UpdatePostByAuthor(post *Post, authorID int64) error {
	if post == nil {
		return errors.New("operation failed")
	}
	if authorID == 0 {
		return errors.New("operation failed")
	}
	if post.Slug == "" {
		post.Slug = slugify(post.Title)
	}
	if post.Slug == "" {
		post.Slug = fmt.Sprintf("post-%d", post.ID)
	}
	slug, err := s.ensureUniqueSlugForID(post.Slug, post.ID)
	if err != nil {
		return err
	}
	post.Slug = slug
	post.UpdatedAt = time.Now()

	res, err := s.db.Exec(
		`UPDATE posts
		 SET title = ?, slug = ?, summary = ?, content = ?, kind = ?, cover_url = ?, published_at = ?, updated_at = ?, is_public = ?
		 WHERE id = ? AND author_id = ?;`,
		post.Title, post.Slug, post.Summary, post.Content, post.Kind, post.CoverURL,
		post.PublishedAt.Unix(), post.UpdatedAt.Unix(), boolToInt(post.IsPublic), post.ID, authorID,
	)
	if err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeletePost 按 ID 删除文章或页面。
func (s *Store) DeletePost(id int64) error {
	_, err := s.db.Exec(`DELETE FROM posts WHERE id = ?;`, id)
	return err
}

// DeletePostByAuthor 删除作者本人名下的文章或页面。
func (s *Store) DeletePostByAuthor(id, authorID int64) error {
	res, err := s.db.Exec(`DELETE FROM posts WHERE id = ? AND author_id = ?;`, id, authorID)
	if err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// nextSortOrder 计算下一个排序值。
func (s *Store) nextSortOrder(kind string, authorID int64) (int64, error) {
	var next int64
	err := s.db.QueryRow(
		`SELECT COALESCE(MAX(sort_order), 0) + 1
		 FROM posts
		 WHERE kind = ? AND author_id = ?;`,
		kind,
		authorID,
	).Scan(&next)
	if err != nil {
		return 0, fmt.Errorf("store error: %w", err)
	}
	return next, nil
}

// ReorderPages 重排页面顺序。
// ids 为排序后的页面 ID 列表。
func (s *Store) ReorderPages(ids []int64) error {
	if len(ids) == 0 {
		return errors.New("operation failed")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	defer tx.Rollback()

	for idx, id := range ids {
		res, execErr := tx.Exec(`UPDATE posts SET sort_order = ? WHERE id = ? AND kind = 'page';`, idx+1, id)
		if execErr != nil {
			return fmt.Errorf("store error: %w", execErr)
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			return sql.ErrNoRows
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	return nil
}

// ReorderPagesByAuthor 重排作者自己的页面顺序。

func (s *Store) ReorderPagesByAuthor(authorID int64, ids []int64) error {
	if authorID == 0 {
		return errors.New("invalid author id")
	}
	if len(ids) == 0 {
		return errors.New("operation failed")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	defer tx.Rollback()

	for idx, id := range ids {
		res, execErr := tx.Exec(
			`UPDATE posts SET sort_order = ? WHERE id = ? AND kind = 'page' AND author_id = ?;`,
			idx+1,
			id,
			authorID,
		)
		if execErr != nil {
			return fmt.Errorf("store error: %w", execErr)
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			return sql.ErrNoRows
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	return nil
}

// GetPostBySlug 根据 slug 获取文章或页面。
func (s *Store) GetPostBySlug(slug string) (*Post, error) {
	var post Post
	var publishedAt, updated int64
	var isPublic int
	err := s.db.QueryRow(
		`SELECT id, title, slug, summary, content, kind, cover_url, published_at, updated_at, author_id, sort_order, is_public, views
		 FROM posts WHERE slug = ?;`,
		slug,
	).Scan(&post.ID, &post.Title, &post.Slug, &post.Summary, &post.Content, &post.Kind, &post.CoverURL, &publishedAt, &updated, &post.AuthorID, &post.SortOrder, &isPublic, &post.Views)
	if err != nil {
		return nil, err
	}
	post.PublishedAt = unixToTime(publishedAt)
	post.UpdatedAt = unixToTime(updated)
	post.IsPublic = isPublic == 1
	return &post, nil
}

// GetPostByID 根据 ID 获取文章或页面。
func (s *Store) GetPostByID(id int64) (*Post, error) {
	var post Post
	var publishedAt, updated int64
	var isPublic int
	err := s.db.QueryRow(
		`SELECT id, title, slug, summary, content, kind, cover_url, published_at, updated_at, author_id, sort_order, is_public, views
		 FROM posts WHERE id = ?;`,
		id,
	).Scan(&post.ID, &post.Title, &post.Slug, &post.Summary, &post.Content, &post.Kind, &post.CoverURL, &publishedAt, &updated, &post.AuthorID, &post.SortOrder, &isPublic, &post.Views)
	if err != nil {
		return nil, err
	}
	post.PublishedAt = unixToTime(publishedAt)
	post.UpdatedAt = unixToTime(updated)
	post.IsPublic = isPublic == 1
	return &post, nil
}

// GetPostByIDAndAuthor 根据 ID 和作者获取文章或页面。
func (s *Store) GetPostByIDAndAuthor(id, authorID int64) (*Post, error) {
	var post Post
	var publishedAt, updated int64
	var isPublic int
	err := s.db.QueryRow(
		`SELECT id, title, slug, summary, content, kind, cover_url, published_at, updated_at, author_id, sort_order, is_public, views
		 FROM posts WHERE id = ? AND author_id = ?;`,
		id,
		authorID,
	).Scan(&post.ID, &post.Title, &post.Slug, &post.Summary, &post.Content, &post.Kind, &post.CoverURL, &publishedAt, &updated, &post.AuthorID, &post.SortOrder, &isPublic, &post.Views)
	if err != nil {
		return nil, err
	}
	post.PublishedAt = unixToTime(publishedAt)
	post.UpdatedAt = unixToTime(updated)
	post.IsPublic = isPublic == 1
	return &post, nil
}

// ListPosts 获取文章列表。
func (s *Store) ListPosts(kind string, limit int) ([]Post, error) {
	defer s.logSlow("ListPosts", time.Now())

	if limit > 0 {
		rows, err := s.db.Query(
			`SELECT id, title, slug, summary, content, kind, cover_url, published_at, updated_at, author_id, sort_order, is_public, views
			 FROM posts
			 WHERE kind = ? AND is_public = 1
			 ORDER BY published_at DESC
			 LIMIT ?;`,
			kind,
			limit,
		)
		if err != nil {
			return nil, fmt.Errorf("store error: %w", err)
		}
		defer rows.Close()
		return scanPosts(rows)
	}

	rows, err := s.db.Query(
		`SELECT id, title, slug, summary, content, kind, cover_url, published_at, updated_at, author_id, sort_order, is_public, views
		 FROM posts
		 WHERE kind = ? AND is_public = 1
		 ORDER BY published_at DESC;`,
		kind,
	)
	if err != nil {
		return nil, fmt.Errorf("store error: %w", err)
	}
	defer rows.Close()
	return scanPosts(rows)
}

// ListPostsAdmin 获取后台文章列表。
func (s *Store) ListPostsAdmin(kind string) ([]Post, error) {
	defer s.logSlow("ListPostsAdmin", time.Now())

	orderBy := "published_at DESC"
	if kind == "page" {
		orderBy = "sort_order ASC, updated_at DESC"
	}

	rows, err := s.db.Query(
		`SELECT id, title, slug, summary, content, kind, cover_url, published_at, updated_at, author_id, sort_order, is_public, views
		 FROM posts WHERE kind = ? ORDER BY `+orderBy+`;`,
		kind,
	)
	if err != nil {
		return nil, fmt.Errorf("store error: %w", err)
	}
	defer rows.Close()
	return scanPosts(rows)
}

// ListPostsByAuthor 获取指定作者的文章列表。
func (s *Store) ListPostsByAuthor(kind string, authorID int64) ([]Post, error) {
	defer s.logSlow("ListPostsByAuthor", time.Now())

	orderBy := "updated_at DESC"
	if kind == "page" {
		orderBy = "sort_order ASC, updated_at DESC"
	}

	rows, err := s.db.Query(
		`SELECT id, title, slug, summary, content, kind, cover_url, published_at, updated_at, author_id, sort_order, is_public, views
		 FROM posts
		 WHERE kind = ? AND author_id = ?
		 ORDER BY `+orderBy+`;`,
		kind,
		authorID,
	)
	if err != nil {
		return nil, fmt.Errorf("store error: %w", err)
	}
	defer rows.Close()
	return scanPosts(rows)
}

// ListPages 返回按导航顺序排列的页面列表。
func (s *Store) ListPages() ([]Post, error) {
	defer s.logSlow("ListPages", time.Now())

	rows, err := s.db.Query(
		`SELECT id, title, slug, summary, content, kind, cover_url, published_at, updated_at, author_id, sort_order, is_public, views
		 FROM posts WHERE kind = 'page' ORDER BY sort_order ASC, updated_at DESC;`,
	)
	if err != nil {
		return nil, fmt.Errorf("store error: %w", err)
	}
	defer rows.Close()
	return scanPosts(rows)
}

// SearchPosts 搜索文章。
func (s *Store) SearchPosts(keyword string) ([]Post, error) {
	defer s.logSlow("SearchPosts", time.Now())

	like := fmt.Sprintf("%%%s%%", strings.TrimSpace(keyword))
	rows, err := s.db.Query(
		`SELECT id, title, slug, summary, content, kind, cover_url, published_at, updated_at, author_id, sort_order, is_public, views
		 FROM posts
		 WHERE kind = 'post' AND is_public = 1 AND (title LIKE ? OR summary LIKE ? OR content LIKE ?)
		 ORDER BY
			CASE
				WHEN title LIKE ? THEN 0
				WHEN summary LIKE ? THEN 1
				ELSE 2
			END ASC,
			published_at DESC;`,
		like,
		like,
		like,
		like,
		like,
	)
	if err != nil {
		return nil, fmt.Errorf("store error: %w", err)
	}
	defer rows.Close()
	return scanPosts(rows)
}

// SearchPages 搜索页面。
func (s *Store) SearchPages(keyword string) ([]Post, error) {
	defer s.logSlow("SearchPages", time.Now())

	like := fmt.Sprintf("%%%s%%", strings.TrimSpace(keyword))
	rows, err := s.db.Query(
		`SELECT id, title, slug, summary, content, kind, cover_url, published_at, updated_at, author_id, sort_order, is_public, views
		 FROM posts
		 WHERE kind = 'page' AND is_public = 1 AND (title LIKE ? OR summary LIKE ? OR content LIKE ?)
		 ORDER BY
			CASE
				WHEN title LIKE ? THEN 0
				WHEN summary LIKE ? THEN 1
				ELSE 2
			END ASC,
			updated_at DESC;`,
		like,
		like,
		like,
		like,
		like,
	)
	if err != nil {
		return nil, fmt.Errorf("store error: %w", err)
	}
	defer rows.Close()
	return scanPosts(rows)
}

// IncrementViews 增加浏览量。
func (s *Store) IncrementViews(id int64) error {
	_, err := s.db.Exec(`UPDATE posts SET views = views + 1 WHERE id = ?;`, id)
	return err
}

// CreateComment 创建评论。
func (s *Store) CreateComment(postID, userID int64, author, content, ip string, anonymous bool) (int64, error) {
	author = strings.TrimSpace(author)
	content = strings.TrimSpace(content)
	ip = strings.TrimSpace(ip)

	if postID == 0 {
		return 0, errors.New("operation failed")
	}
	if content == "" {
		return 0, errors.New("comment content is required")
	}
	if len(ip) > 128 {
		ip = ip[:128]
	}

	if anonymous {
		author = "Anonymous"
	} else if userID > 0 && author == "" {
		_ = s.db.QueryRow(`SELECT display_name FROM users WHERE id = ?;`, userID).Scan(&author)
		if strings.TrimSpace(author) == "" {
			author = "User"
		}
	} else if author == "" {
		author = "Guest"
	}

	res, err := s.db.Exec(
		`INSERT INTO comments (post_id, user_id, author, content, ip, is_anonymous, is_hidden, likes, dislikes, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, 0, 0, 0, ?);`,
		postID, userID, author, content, ip, boolToInt(anonymous), time.Now().Unix(),
	)
	if err != nil {
		return 0, fmt.Errorf("store error: %w", err)
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// ListComments 返回文章下可见的评论。
func (s *Store) ListComments(postID int64) ([]Comment, error) {
	defer s.logSlow("ListComments", time.Now())

	rows, err := s.db.Query(
		`SELECT c.id, c.post_id, c.user_id, c.author, c.content, c.ip, c.is_anonymous, c.is_hidden, c.likes, c.dislikes, c.created_at,
				COALESCE(u.avatar_url, '')
		 FROM comments c
		 LEFT JOIN users u ON u.id = c.user_id
		 WHERE c.post_id = ? AND c.is_hidden = 0
		 ORDER BY c.created_at DESC;`,
		postID,
	)
	if err != nil {
		return nil, fmt.Errorf("store error: %w", err)
	}
	defer rows.Close()
	return scanComments(rows)
}

// ListCommentsAdmin 返回后台可见的全部评论。
func (s *Store) ListCommentsAdmin(postID int64) ([]Comment, error) {
	defer s.logSlow("ListCommentsAdmin", time.Now())

	rows, err := s.db.Query(
		`SELECT c.id, c.post_id, c.user_id, c.author, c.content, c.ip, c.is_anonymous, c.is_hidden, c.likes, c.dislikes, c.created_at,
				COALESCE(u.avatar_url, '')
		 FROM comments c
		 LEFT JOIN users u ON u.id = c.user_id
		 WHERE c.post_id = ?
		 ORDER BY c.created_at DESC;`,
		postID,
	)
	if err != nil {
		return nil, fmt.Errorf("store error: %w", err)
	}
	defer rows.Close()
	return scanComments(rows)
}

// ListCommentsByUser 返回用户自己的评论列表。
func (s *Store) ListCommentsByUser(userID int64) ([]Comment, error) {
	defer s.logSlow("ListCommentsByUser", time.Now())

	rows, err := s.db.Query(
		`SELECT c.id, c.post_id, c.user_id, c.author, c.content, c.ip, c.is_anonymous, c.is_hidden, c.likes, c.dislikes, c.created_at,
				COALESCE(u.avatar_url, ''), COALESCE(p.title, ''), COALESCE(p.slug, '')
		 FROM comments c
		 LEFT JOIN users u ON u.id = c.user_id
		 LEFT JOIN posts p ON p.id = c.post_id
		 WHERE c.user_id = ?
		 ORDER BY c.created_at DESC;`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("store error: %w", err)
	}
	defer rows.Close()

	var comments []Comment
	for rows.Next() {
		var c Comment
		var isAnonymous, isHidden int
		var createdAt int64
		if err := rows.Scan(&c.ID, &c.PostID, &c.UserID, &c.Author, &c.Content, &c.IP, &isAnonymous, &isHidden, &c.Likes, &c.Dislikes, &createdAt, &c.AvatarURL, &c.PostTitle, &c.PostSlug); err != nil {
			return nil, fmt.Errorf("store error: %w", err)
		}
		c.IsAnonymous = isAnonymous == 1
		c.IsHidden = isHidden == 1
		c.CreatedAt = unixToTime(createdAt)
		comments = append(comments, c)
	}
	return comments, nil
}

// UpdateCommentContentByUser 允许用户更新自己的评论内容。
func (s *Store) UpdateCommentContentByUser(id, userID int64, content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return errors.New("comment content is required")
	}
	res, err := s.db.Exec(`UPDATE comments SET content = ? WHERE id = ? AND user_id = ?;`, content, id, userID)
	if err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteCommentByUser 允许用户删除自己的评论。
func (s *Store) DeleteCommentByUser(id, userID int64) error {
	res, err := s.db.Exec(`DELETE FROM comments WHERE id = ? AND user_id = ?;`, id, userID)
	if err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// SetCommentHidden 设置评论隐藏状态。
func (s *Store) SetCommentHidden(id int64, hidden bool) error {
	_, err := s.db.Exec(`UPDATE comments SET is_hidden = ? WHERE id = ?;`, boolToInt(hidden), id)
	if err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	return nil
}

// DeleteComment 删除评论。
func (s *Store) DeleteComment(id int64) error {
	_, err := s.db.Exec(`DELETE FROM comments WHERE id = ?;`, id)
	if err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	return nil
}

// ReactComment 记录对可见评论的点赞或点踩。
func (s *Store) ReactComment(postID, commentID int64, action string) error {
	action = strings.TrimSpace(strings.ToLower(action))
	query := ""
	switch action {
	case "like":
		query = `UPDATE comments SET likes = likes + 1 WHERE id = ? AND post_id = ? AND is_hidden = 0;`
	case "dislike":
		query = `UPDATE comments SET dislikes = dislikes + 1 WHERE id = ? AND post_id = ? AND is_hidden = 0;`
	default:
		return errors.New("invalid comment reaction")
	}
	res, err := s.db.Exec(query, commentID, postID)
	if err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// SetUserSetting 保存用户设置。
func (s *Store) SetUserSetting(userID int64, key, value string) error {
	if userID == 0 {
		return errors.New("operation failed")
	}
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" {
		return errors.New("setting key is required")
	}
	_, err := s.db.Exec(
		`INSERT INTO user_settings (user_id, key, value) VALUES (?, ?, ?)
		 ON CONFLICT(user_id, key) DO UPDATE SET value = excluded.value;`,
		userID, key, value,
	)
	if err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	return nil
}

// GetUserSetting 读取用户设置。
func (s *Store) GetUserSetting(userID int64, key string) (string, error) {
	if userID == 0 {
		return "", errors.New("operation failed")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", errors.New("setting key is required")
	}
	var value string
	err := s.db.QueryRow(`SELECT value FROM user_settings WHERE user_id = ? AND key = ?;`, userID, key).Scan(&value)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("store error: %w", err)
	}
	return value, nil
}

// CreateAvatarRequest 创建头像审核申请。
func (s *Store) CreateAvatarRequest(userID int64, avatarURL string) (int64, error) {
	if userID == 0 {
		return 0, errors.New("operation failed")
	}
	avatarURL = strings.TrimSpace(avatarURL)
	if avatarURL == "" {
		return 0, errors.New("avatar URL is required")
	}

	res, err := s.db.Exec(
		`INSERT INTO avatar_requests (user_id, avatar_url, status, review_note, created_at, reviewed_at, reviewer_user_id)
		 VALUES (?, ?, ?, '', ?, 0, 0);`,
		userID, avatarURL, AvatarStatusPending, time.Now().Unix(),
	)
	if err != nil {
		return 0, fmt.Errorf("store error: %w", err)
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// ListAvatarRequests 返回头像审核申请列表。
func (s *Store) ListAvatarRequests(status string, limit int) ([]AvatarRequest, error) {
	if limit <= 0 {
		limit = 50
	}
	status = strings.TrimSpace(status)

	var (
		rows *sql.Rows
		err  error
	)
	if status == "" {
		rows, err = s.db.Query(
			`SELECT r.id, r.user_id, u.username, u.display_name, r.avatar_url, r.status, r.review_note,
					r.created_at, r.reviewed_at, r.reviewer_user_id, COALESCE(reviewer.username, '')
			 FROM avatar_requests r
			 JOIN users u ON u.id = r.user_id
			 LEFT JOIN users reviewer ON reviewer.id = r.reviewer_user_id
			 ORDER BY r.created_at DESC
			 LIMIT ?;`,
			limit,
		)
	} else {
		rows, err = s.db.Query(
			`SELECT r.id, r.user_id, u.username, u.display_name, r.avatar_url, r.status, r.review_note,
					r.created_at, r.reviewed_at, r.reviewer_user_id, COALESCE(reviewer.username, '')
			 FROM avatar_requests r
			 JOIN users u ON u.id = r.user_id
			 LEFT JOIN users reviewer ON reviewer.id = r.reviewer_user_id
			 WHERE r.status = ?
			 ORDER BY r.created_at DESC
			 LIMIT ?;`,
			status,
			limit,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("store error: %w", err)
	}
	defer rows.Close()
	return scanAvatarRequests(rows)
}

// ListAvatarRequestsByUser 返回某个用户的头像申请列表。
func (s *Store) ListAvatarRequestsByUser(userID int64, limit int) ([]AvatarRequest, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(
		`SELECT r.id, r.user_id, u.username, u.display_name, r.avatar_url, r.status, r.review_note,
				r.created_at, r.reviewed_at, r.reviewer_user_id, COALESCE(reviewer.username, '')
		 FROM avatar_requests r
		 JOIN users u ON u.id = r.user_id
		 LEFT JOIN users reviewer ON reviewer.id = r.reviewer_user_id
		 WHERE r.user_id = ?
		 ORDER BY r.created_at DESC
		 LIMIT ?;`,
		userID,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("store error: %w", err)
	}
	defer rows.Close()
	return scanAvatarRequests(rows)
}

// ReviewAvatarRequest 审核头像申请。
func (s *Store) ReviewAvatarRequest(requestID, reviewerUserID int64, approved bool, reviewNote string) error {
	if requestID == 0 || reviewerUserID == 0 {
		return errors.New("operation failed")
	}
	reviewNote = strings.TrimSpace(reviewNote)
	status := AvatarStatusRejected
	if approved {
		status = AvatarStatusApproved
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	defer tx.Rollback()

	var (
		userID    int64
		avatarURL string
		oldStatus string
	)
	if err := tx.QueryRow(`SELECT user_id, avatar_url, status FROM avatar_requests WHERE id = ?;`, requestID).Scan(&userID, &avatarURL, &oldStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("avatar request not found")
		}
		return fmt.Errorf("store error: %w", err)
	}
	if oldStatus != AvatarStatusPending {
		return errors.New("operation failed")
	}

	_, err = tx.Exec(
		`UPDATE avatar_requests
		 SET status = ?, review_note = ?, reviewed_at = ?, reviewer_user_id = ?
		 WHERE id = ?;`,
		status, reviewNote, time.Now().Unix(), reviewerUserID, requestID,
	)
	if err != nil {
		return fmt.Errorf("store error: %w", err)
	}

	if approved {
		if _, err := tx.Exec(`UPDATE users SET avatar_url = ? WHERE id = ?;`, avatarURL, userID); err != nil {
			return fmt.Errorf("store error: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	return nil
}

// GetSettings 读取全部站点设置。
func (s *Store) GetSettings() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT key, value FROM settings;`)
	if err != nil {
		return nil, fmt.Errorf("store error: %w", err)
	}
	defer rows.Close()

	settings := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("store error: %w", err)
		}
		settings[key] = value
	}
	return settings, nil
}

// SetSetting 写入或更新站点设置。
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value;`,
		key,
		value,
	)
	if err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	return nil
}

// GetStats 获取站点统计信息。
func (s *Store) GetStats() (int, int, error) {
	var posts, views int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM posts WHERE kind='post';`).Scan(&posts); err != nil {
		return 0, 0, err
	}
	if err := s.db.QueryRow(`SELECT COALESCE(SUM(views), 0) FROM posts WHERE kind='post';`).Scan(&views); err != nil {
		return 0, 0, err
	}
	return posts, views, nil
}

func scanPosts(rows *sql.Rows) ([]Post, error) {
	var posts []Post
	for rows.Next() {
		var p Post
		var publishedAt, updated int64
		var isPublic int
		if err := rows.Scan(&p.ID, &p.Title, &p.Slug, &p.Summary, &p.Content, &p.Kind, &p.CoverURL, &publishedAt, &updated, &p.AuthorID, &p.SortOrder, &isPublic, &p.Views); err != nil {
			return nil, fmt.Errorf("store error: %w", err)
		}
		p.PublishedAt = unixToTime(publishedAt)
		p.UpdatedAt = unixToTime(updated)
		p.IsPublic = isPublic == 1
		posts = append(posts, p)
	}
	return posts, nil
}

func scanComments(rows *sql.Rows) ([]Comment, error) {
	var comments []Comment
	for rows.Next() {
		var c Comment
		var isAnonymous, isHidden int
		var createdAt int64
		if err := rows.Scan(&c.ID, &c.PostID, &c.UserID, &c.Author, &c.Content, &c.IP, &isAnonymous, &isHidden, &c.Likes, &c.Dislikes, &createdAt, &c.AvatarURL); err != nil {
			return nil, fmt.Errorf("store error: %w", err)
		}
		c.IsAnonymous = isAnonymous == 1
		c.IsHidden = isHidden == 1
		c.CreatedAt = unixToTime(createdAt)
		comments = append(comments, c)
	}
	return comments, nil
}

func scanAvatarRequests(rows *sql.Rows) ([]AvatarRequest, error) {
	var result []AvatarRequest
	for rows.Next() {
		var item AvatarRequest
		var createdAt, reviewedAt int64
		if err := rows.Scan(&item.ID, &item.UserID, &item.Username, &item.DisplayName, &item.AvatarURL, &item.Status, &item.ReviewNote, &createdAt, &reviewedAt, &item.ReviewerID, &item.ReviewerName); err != nil {
			return nil, fmt.Errorf("store error: %w", err)
		}
		item.CreatedAt = unixToTime(createdAt)
		item.ReviewedAt = unixToTime(reviewedAt)
		result = append(result, item)
	}
	return result, nil
}

func slugify(title string) string {
	lower := strings.ToLower(strings.TrimSpace(title))
	lower = strings.ReplaceAll(lower, " ", "-")
	lower = regexp.MustCompile(`[^a-z0-9\-]`).ReplaceAllString(lower, "")
	lower = strings.Trim(lower, "-")
	return lower
}

func (s *Store) ensureUniqueSlug(slug string) (string, error) {
	base := slug
	for i := 0; i < 50; i++ {
		exists, err := s.slugExists(slug)
		if err != nil {
			return "", err
		}
		if !exists {
			return slug, nil
		}
		slug = fmt.Sprintf("%s-%d", base, i+1)
	}
	return "", errors.New("operation failed")
}

func (s *Store) ensureUniqueSlugForID(slug string, id int64) (string, error) {
	base := slug
	for i := 0; i < 50; i++ {
		exists, err := s.slugExistsForID(slug, id)
		if err != nil {
			return "", err
		}
		if !exists {
			return slug, nil
		}
		slug = fmt.Sprintf("%s-%d", base, i+1)
	}
	return "", errors.New("operation failed")
}

func (s *Store) slugExists(slug string) (bool, error) {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM posts WHERE slug = ?;`, slug).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *Store) slugExistsForID(slug string, id int64) (bool, error) {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM posts WHERE slug = ? AND id != ?;`, slug, id).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func normalizeRole(role string) string {
	switch strings.TrimSpace(strings.ToLower(role)) {
	case RoleOwner:
		return RoleOwner
	case RoleMaintainer:
		return RoleMaintainer
	default:
		return RoleVisitor
	}
}

func unixToTime(sec int64) time.Time {
	if sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}

func (s *Store) logSlow(op string, start time.Time) {
	if s == nil {
		return
	}
	elapsed := time.Since(start)
	if elapsed >= s.slowQueryThreshold {
		log.Printf("[WARN] slow query op=%s elapsed=%s", op, elapsed.String())
	}
}

// hashPasswordArgon2id 使用 Argon2id 生成密码哈希。

func hashPasswordArgon2id(password string) (string, error) {
	password = strings.TrimSpace(password)
	if password == "" {
		return "", errors.New("operation failed")
	}

	salt := make([]byte, argon2SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("store error: %w", err)
	}

	key := argon2.IDKey([]byte(password), salt, argon2Iterations, argon2MemoryKB, argon2Parallel, argon2KeyLength)
	saltB64 := base64.RawStdEncoding.EncodeToString(salt)
	keyB64 := base64.RawStdEncoding.EncodeToString(key)
	encoded := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argon2MemoryKB, argon2Iterations, argon2Parallel, saltB64, keyB64)
	return encoded, nil
}

// verifyPassword 校验密码哈希。
// 1) ok 表示密码是否匹配。
// 2) needUpgrade 表示哈希是否需要升级。
// 3) err 表示校验过程中是否出错。
func verifyPassword(storedHash, password string) (ok bool, needUpgrade bool, err error) {
	if strings.HasPrefix(storedHash, "$argon2id$") {
		params, salt, expected, parseErr := parseArgon2Hash(storedHash)
		if parseErr != nil {
			return false, false, parseErr
		}
		actual := argon2.IDKey([]byte(password), salt, params.iterations, params.memoryKB, params.parallel, uint32(len(expected)))
		if subtle.ConstantTimeCompare(expected, actual) == 1 {
			return true, false, nil
		}
		return false, false, nil
	}

	// bcrypt 兼容旧版密码哈希。
	if bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(password)) == nil {
		return true, true, nil
	}
	return false, false, nil
}

type argon2Params struct {
	memoryKB   uint32
	iterations uint32
	parallel   uint8
}

func parseArgon2Hash(encoded string) (argon2Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 {
		return argon2Params{}, nil, nil, errors.New("invalid argon2 hash format")
	}
	if parts[1] != "argon2id" {
		return argon2Params{}, nil, nil, errors.New("operation failed")
	}

	versionPart := parts[2]
	if !strings.HasPrefix(versionPart, "v=") {
		return argon2Params{}, nil, nil, errors.New("operation failed")
	}
	version, err := strconv.Atoi(strings.TrimPrefix(versionPart, "v="))
	if err != nil || version != argon2.Version {
		return argon2Params{}, nil, nil, errors.New("operation failed")
	}

	paramsPart := strings.Split(parts[3], ",")
	if len(paramsPart) != 3 {
		return argon2Params{}, nil, nil, errors.New("operation failed")
	}

	var p argon2Params
	for _, piece := range paramsPart {
		piece = strings.TrimSpace(piece)
		switch {
		case strings.HasPrefix(piece, "m="):
			v, convErr := strconv.ParseUint(strings.TrimPrefix(piece, "m="), 10, 32)
			if convErr != nil {
				return argon2Params{}, nil, nil, errors.New("operation failed")
			}
			p.memoryKB = uint32(v)
		case strings.HasPrefix(piece, "t="):
			v, convErr := strconv.ParseUint(strings.TrimPrefix(piece, "t="), 10, 32)
			if convErr != nil {
				return argon2Params{}, nil, nil, errors.New("operation failed")
			}
			p.iterations = uint32(v)
		case strings.HasPrefix(piece, "p="):
			v, convErr := strconv.ParseUint(strings.TrimPrefix(piece, "p="), 10, 8)
			if convErr != nil {
				return argon2Params{}, nil, nil, errors.New("operation failed")
			}
			p.parallel = uint8(v)
		default:
			return argon2Params{}, nil, nil, errors.New("operation failed")
		}
	}
	if p.memoryKB == 0 || p.iterations == 0 || p.parallel == 0 {
		return argon2Params{}, nil, nil, errors.New("operation failed")
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return argon2Params{}, nil, nil, errors.New("invalid argon2 salt encoding")
	}
	hash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return argon2Params{}, nil, nil, errors.New("operation failed")
	}
	if len(salt) == 0 || len(hash) == 0 {
		return argon2Params{}, nil, nil, errors.New("operation failed")
	}

	return p, salt, hash, nil
}

type schemaMigration struct {
	version int
	name    string
	stmts   []string
}

func (s *Store) applyMigrations() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at INTEGER NOT NULL
	);`); err != nil {
		return fmt.Errorf("store error: %w", err)
	}

	migrations := []schemaMigration{
		{
			version: 1,
			name:    "create_comment_reports",
			stmts: []string{
				`CREATE TABLE IF NOT EXISTS comment_reports (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					comment_id INTEGER NOT NULL,
					reporter_user_id INTEGER NOT NULL,
					reason TEXT NOT NULL,
					status TEXT NOT NULL DEFAULT 'pending',
					review_note TEXT NOT NULL DEFAULT '',
					reviewer_user_id INTEGER NOT NULL DEFAULT 0,
					created_at INTEGER NOT NULL,
					reviewed_at INTEGER NOT NULL DEFAULT 0,
					FOREIGN KEY(comment_id) REFERENCES comments(id) ON DELETE CASCADE,
					FOREIGN KEY(reporter_user_id) REFERENCES users(id) ON DELETE CASCADE
				);`,
				`CREATE INDEX IF NOT EXISTS idx_comment_reports_status_created ON comment_reports (status, created_at DESC);`,
				`CREATE INDEX IF NOT EXISTS idx_comment_reports_reporter_created ON comment_reports (reporter_user_id, created_at DESC);`,
			},
		},
		{
			version: 2,
			name:    "create_notifications",
			stmts: []string{
				`CREATE TABLE IF NOT EXISTS notifications (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					user_id INTEGER NOT NULL,
					title TEXT NOT NULL,
					content TEXT NOT NULL,
					kind TEXT NOT NULL DEFAULT 'system',
					is_read INTEGER NOT NULL DEFAULT 0,
					created_at INTEGER NOT NULL,
					FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
				);`,
				`CREATE INDEX IF NOT EXISTS idx_notifications_user_read_created ON notifications (user_id, is_read, created_at DESC);`,
			},
		},
		{
			version: 3,
			name:    "create_password_reset_tokens",
			stmts: []string{
				`CREATE TABLE IF NOT EXISTS password_reset_tokens (
					token TEXT PRIMARY KEY,
					user_id INTEGER NOT NULL,
					expires_at INTEGER NOT NULL,
					created_at INTEGER NOT NULL,
					used_at INTEGER NOT NULL DEFAULT 0,
					FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
				);`,
				`CREATE INDEX IF NOT EXISTS idx_password_reset_tokens_user_created ON password_reset_tokens (user_id, created_at DESC);`,
			},
		},
		{
			version: 4,
			name:    "create_email_verify_tokens",
			stmts: []string{
				`CREATE TABLE IF NOT EXISTS email_verify_tokens (
					token TEXT PRIMARY KEY,
					user_id INTEGER NOT NULL,
					email TEXT NOT NULL,
					expires_at INTEGER NOT NULL,
					created_at INTEGER NOT NULL,
					used_at INTEGER NOT NULL DEFAULT 0,
					FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
				);`,
				`CREATE INDEX IF NOT EXISTS idx_email_verify_tokens_user_created ON email_verify_tokens (user_id, created_at DESC);`,
			},
		},
		{
			version: 5,
			name:    "create_post_reports",
			stmts: []string{
				`CREATE TABLE IF NOT EXISTS post_reports (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					post_id INTEGER NOT NULL,
					reporter_user_id INTEGER NOT NULL,
					reason TEXT NOT NULL,
					status TEXT NOT NULL DEFAULT 'pending',
					review_note TEXT NOT NULL DEFAULT '',
					reviewer_user_id INTEGER NOT NULL DEFAULT 0,
					created_at INTEGER NOT NULL,
					reviewed_at INTEGER NOT NULL DEFAULT 0,
					FOREIGN KEY(post_id) REFERENCES posts(id) ON DELETE CASCADE,
					FOREIGN KEY(reporter_user_id) REFERENCES users(id) ON DELETE CASCADE
				);`,
				`CREATE INDEX IF NOT EXISTS idx_post_reports_status_created ON post_reports (status, created_at DESC);`,
				`CREATE INDEX IF NOT EXISTS idx_post_reports_reporter_created ON post_reports (reporter_user_id, created_at DESC);`,
			},
		},
	}

	for _, m := range migrations {
		var count int
		if err := s.db.QueryRow(`SELECT COUNT(1) FROM schema_migrations WHERE version = ?;`, m.version).Scan(&count); err != nil {
			return fmt.Errorf("store error on %d: %w", m.version, err)
		}
		if count > 0 {
			continue
		}

		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("store error on %d: %w", m.version, err)
		}
		for _, stmt := range m.stmts {
			if _, err := tx.Exec(stmt); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("store error on %d (%s): %w", m.version, m.name, err)
			}
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?);`, m.version, m.name, time.Now().Unix()); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store error on %d: %w", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store error on %d: %w", m.version, err)
		}
	}
	return nil
}

func randomTokenURLSafe(n int) (string, error) {
	if n <= 0 {
		n = 32
	}
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func (s *Store) CreateNotification(userID int64, title, content, kind string) error {
	if userID == 0 {
		return errors.New("operation failed")
	}
	title = strings.TrimSpace(title)
	content = strings.TrimSpace(content)
	kind = strings.TrimSpace(kind)
	if title == "" || content == "" {
		return errors.New("notification title and content are required")
	}
	if kind == "" {
		kind = "system"
	}
	_, err := s.db.Exec(
		`INSERT INTO notifications (user_id, title, content, kind, is_read, created_at)
		 VALUES (?, ?, ?, ?, 0, ?);`,
		userID, title, content, kind, time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	return nil
}

func (s *Store) ListNotificationsByUser(userID int64, limit int) ([]Notification, error) {
	defer s.logSlow("ListNotificationsByUser", time.Now())

	if userID == 0 {
		return nil, errors.New("operation failed")
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT id, user_id, title, content, kind, is_read, created_at
		 FROM notifications
		 WHERE user_id = ?
		 ORDER BY created_at DESC
		 LIMIT ?;`,
		userID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list notifications: %w", err)
	}
	defer rows.Close()

	var list []Notification
	for rows.Next() {
		var n Notification
		var createdAt int64
		var isRead int
		if err := rows.Scan(&n.ID, &n.UserID, &n.Title, &n.Content, &n.Kind, &isRead, &createdAt); err != nil {
			return nil, fmt.Errorf("scan notifications: %w", err)
		}
		n.IsRead = isRead == 1
		n.CreatedAt = unixToTime(createdAt)
		list = append(list, n)
	}
	return list, nil
}

func (s *Store) CountUnreadNotifications(userID int64) (int, error) {
	if userID == 0 {
		return 0, errors.New("operation failed")
	}
	var c int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM notifications WHERE user_id = ? AND is_read = 0;`, userID).Scan(&c); err != nil {
		return 0, fmt.Errorf("count unread notifications: %w", err)
	}
	return c, nil
}

func (s *Store) MarkNotificationRead(userID, notificationID int64) error {
	if userID == 0 || notificationID == 0 {
		return errors.New("invalid notification parameters")
	}
	_, err := s.db.Exec(`UPDATE notifications SET is_read = 1 WHERE id = ? AND user_id = ?;`, notificationID, userID)
	if err != nil {
		return fmt.Errorf("mark notification read: %w", err)
	}
	return nil
}

func (s *Store) MarkAllNotificationsRead(userID int64) error {
	if userID == 0 {
		return errors.New("operation failed")
	}
	_, err := s.db.Exec(`UPDATE notifications SET is_read = 1 WHERE user_id = ? AND is_read = 0;`, userID)
	if err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	return nil
}

func (s *Store) CreateCommentReport(commentID, reporterUserID int64, reason string) (int64, error) {
	if commentID == 0 || reporterUserID == 0 {
		return 0, errors.New("invalid parameters")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "suspected policy violation"
	}

	var exists int
	if err := s.db.QueryRow(
		`SELECT COUNT(1) FROM comment_reports
		 WHERE comment_id = ? AND reporter_user_id = ? AND status = ?;`,
		commentID, reporterUserID, CommentReportStatusPending,
	).Scan(&exists); err == nil && exists > 0 {
		return 0, errors.New("you already reported this comment")
	}

	res, err := s.db.Exec(
		`INSERT INTO comment_reports (comment_id, reporter_user_id, reason, status, review_note, reviewer_user_id, created_at, reviewed_at)
		 VALUES (?, ?, ?, ?, '', 0, ?, 0);`,
		commentID, reporterUserID, reason, CommentReportStatusPending, time.Now().Unix(),
	)
	if err != nil {
		return 0, fmt.Errorf("create comment report: %w", err)
	}
	id, _ := res.LastInsertId()
	return id, nil
}

func (s *Store) ListCommentReports(status string, limit int) ([]CommentReport, error) {
	defer s.logSlow("ListCommentReports", time.Now())

	if limit <= 0 {
		limit = 100
	}
	status = strings.TrimSpace(status)

	baseSQL := `SELECT r.id, r.comment_id, COALESCE(c.content, ''), r.reporter_user_id,
		COALESCE(u.username, ''), COALESCE(u.display_name, ''), r.reason, r.status,
		r.review_note, r.reviewer_user_id, r.created_at, r.reviewed_at
		FROM comment_reports r
		LEFT JOIN comments c ON c.id = r.comment_id
		LEFT JOIN users u ON u.id = r.reporter_user_id`

	var (
		rows *sql.Rows
		err  error
	)
	if status == "" {
		rows, err = s.db.Query(baseSQL+` ORDER BY r.created_at DESC LIMIT ?;`, limit)
	} else {
		rows, err = s.db.Query(baseSQL+` WHERE r.status = ? ORDER BY r.created_at DESC LIMIT ?;`, status, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("list comment reports: %w", err)
	}
	defer rows.Close()

	var list []CommentReport
	for rows.Next() {
		var item CommentReport
		var createdAt, reviewedAt int64
		if err := rows.Scan(
			&item.ID, &item.CommentID, &item.CommentContent,
			&item.ReporterUserID, &item.ReporterUsername, &item.ReporterDisplayName,
			&item.Reason, &item.Status, &item.ReviewNote, &item.ReviewerID,
			&createdAt, &reviewedAt,
		); err != nil {
			return nil, fmt.Errorf("scan comment reports: %w", err)
		}
		item.CreatedAt = unixToTime(createdAt)
		item.ReviewedAt = unixToTime(reviewedAt)
		list = append(list, item)
	}
	return list, nil
}

func (s *Store) ReviewCommentReport(reportID, reviewerUserID int64, approved bool, note string) error {
	if reportID == 0 || reviewerUserID == 0 {
		return errors.New("invalid review parameters")
	}
	note = strings.TrimSpace(note)
	status := CommentReportStatusRejected
	if approved {
		status = CommentReportStatusApproved
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	defer tx.Rollback()

	var (
		oldStatus      string
		commentID      int64
		reporterUserID int64
		commentUserID  int64
	)
	if err := tx.QueryRow(
		`SELECT r.status, r.comment_id, r.reporter_user_id, COALESCE(c.user_id, 0)
		 FROM comment_reports r
		 LEFT JOIN comments c ON c.id = r.comment_id
		 WHERE r.id = ?;`,
		reportID,
	).Scan(&oldStatus, &commentID, &reporterUserID, &commentUserID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("comment report not found")
		}
		return fmt.Errorf("load comment report: %w", err)
	}
	if oldStatus != CommentReportStatusPending {
		return errors.New("operation failed")
	}

	if _, err := tx.Exec(
		`UPDATE comment_reports
		 SET status = ?, review_note = ?, reviewer_user_id = ?, reviewed_at = ?
		 WHERE id = ?;`,
		status, note, reviewerUserID, time.Now().Unix(), reportID,
	); err != nil {
		return fmt.Errorf("store error: %w", err)
	}

	if approved {
		if _, err := tx.Exec(`UPDATE comments SET is_hidden = 1 WHERE id = ?;`, commentID); err != nil {
			return fmt.Errorf("store error: %w", err)
		}
	}

	if reporterUserID > 0 {
		title := "Your comment report was reviewed"
		content := "The report was rejected."
		if approved {
			content = "The report was approved and the comment was hidden."
		}
		if _, err := tx.Exec(
			`INSERT INTO notifications (user_id, title, content, kind, is_read, created_at)
			 VALUES (?, ?, ?, 'report', 0, ?);`,
			reporterUserID, title, content, time.Now().Unix(),
		); err != nil {
			return fmt.Errorf("store error: %w", err)
		}
	}

	if approved && commentUserID > 0 && commentUserID != reporterUserID {
		if _, err := tx.Exec(
			`INSERT INTO notifications (user_id, title, content, kind, is_read, created_at)
			 VALUES (?, ?, ?, 'report', 0, ?);`,
			commentUserID, "你的留言已处理", "你的留言因被举报，已被管理员隐藏。", time.Now().Unix(),
		); err != nil {
			return fmt.Errorf("store error: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	return nil
}

func (s *Store) CreatePostReport(postID, reporterUserID int64, reason string) (int64, error) {
	if postID == 0 || reporterUserID == 0 {
		return 0, errors.New("invalid parameters")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "suspected policy violation"
	}

	var postCount int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM posts WHERE id = ? AND kind = 'post';`, postID).Scan(&postCount); err != nil {
		return 0, fmt.Errorf("validate post report target: %w", err)
	}
	if postCount == 0 {
		return 0, errors.New("post not found")
	}

	var exists int
	if err := s.db.QueryRow(
		`SELECT COUNT(1) FROM post_reports
		 WHERE post_id = ? AND reporter_user_id = ? AND status = ?;`,
		postID, reporterUserID, CommentReportStatusPending,
	).Scan(&exists); err == nil && exists > 0 {
		return 0, errors.New("you already reported this post")
	}

	res, err := s.db.Exec(
		`INSERT INTO post_reports (post_id, reporter_user_id, reason, status, review_note, reviewer_user_id, created_at, reviewed_at)
		 VALUES (?, ?, ?, ?, '', 0, ?, 0);`,
		postID, reporterUserID, reason, CommentReportStatusPending, time.Now().Unix(),
	)
	if err != nil {
		return 0, fmt.Errorf("create post report: %w", err)
	}
	id, _ := res.LastInsertId()
	return id, nil
}

func (s *Store) ListPostReports(status string, limit int) ([]PostReport, error) {
	if limit <= 0 {
		limit = 100
	}
	status = strings.TrimSpace(status)

	baseSQL := `SELECT r.id, r.post_id, COALESCE(p.title, ''), COALESCE(p.slug, ''), r.reporter_user_id,
		COALESCE(u.username, ''), COALESCE(u.display_name, ''), r.reason, r.status,
		r.review_note, r.reviewer_user_id, r.created_at, r.reviewed_at
		FROM post_reports r
		LEFT JOIN posts p ON p.id = r.post_id
		LEFT JOIN users u ON u.id = r.reporter_user_id`

	var (
		rows *sql.Rows
		err  error
	)
	if status == "" {
		rows, err = s.db.Query(baseSQL+` ORDER BY r.created_at DESC LIMIT ?;`, limit)
	} else {
		rows, err = s.db.Query(baseSQL+` WHERE r.status = ? ORDER BY r.created_at DESC LIMIT ?;`, status, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("读取文章举报列表失败: %w", err)
	}
	defer rows.Close()

	var list []PostReport
	for rows.Next() {
		var item PostReport
		var createdAt, reviewedAt int64
		if err := rows.Scan(
			&item.ID, &item.PostID, &item.PostTitle, &item.PostSlug,
			&item.ReporterUserID, &item.ReporterUsername, &item.ReporterDisplayName,
			&item.Reason, &item.Status, &item.ReviewNote, &item.ReviewerID,
			&createdAt, &reviewedAt,
		); err != nil {
			return nil, fmt.Errorf("解析文章举报列表失败: %w", err)
		}
		item.CreatedAt = unixToTime(createdAt)
		item.ReviewedAt = unixToTime(reviewedAt)
		list = append(list, item)
	}
	return list, nil
}

func (s *Store) ReviewPostReport(reportID, reviewerUserID int64, approved bool, note string) error {
	if reportID == 0 || reviewerUserID == 0 {
		return errors.New("参数无效")
	}
	note = strings.TrimSpace(note)
	status := CommentReportStatusRejected
	if approved {
		status = CommentReportStatusApproved
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("开启审核事务失败: %w", err)
	}
	defer tx.Rollback()

	var (
		oldStatus      string
		postID         int64
		postAuthorID   int64
		reporterUserID int64
	)
	if err := tx.QueryRow(
		`SELECT r.status, r.post_id, COALESCE(p.author_id, 0), r.reporter_user_id
		 FROM post_reports r
		 LEFT JOIN posts p ON p.id = r.post_id
		 WHERE r.id = ?;`,
		reportID,
	).Scan(&oldStatus, &postID, &postAuthorID, &reporterUserID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("举报记录不存在")
		}
		return fmt.Errorf("读取文章举报记录失败: %w", err)
	}
	if oldStatus != CommentReportStatusPending {
		return errors.New("该举报已处理")
	}

	if _, err := tx.Exec(
		`UPDATE post_reports
		 SET status = ?, review_note = ?, reviewer_user_id = ?, reviewed_at = ?
		 WHERE id = ?;`,
		status, note, reviewerUserID, time.Now().Unix(), reportID,
	); err != nil {
		return fmt.Errorf("更新文章举报状态失败: %w", err)
	}

	if approved {
		if _, err := tx.Exec(`UPDATE posts SET is_public = 0 WHERE id = ?;`, postID); err != nil {
			return fmt.Errorf("隐藏被举报文章失败: %w", err)
		}
	}

	if reporterUserID > 0 {
		title := "你的文章举报已处理"
		content := "举报未通过。"
		if approved {
			content = "举报已通过，文章已被隐藏。"
		}
		if _, err := tx.Exec(
			`INSERT INTO notifications (user_id, title, content, kind, is_read, created_at)
			 VALUES (?, ?, ?, 'report', 0, ?);`,
			reporterUserID, title, content, time.Now().Unix(),
		); err != nil {
			return fmt.Errorf("写入举报结果通知失败: %w", err)
		}
	}

	if approved && postAuthorID > 0 && postAuthorID != reporterUserID {
		if _, err := tx.Exec(
			`INSERT INTO notifications (user_id, title, content, kind, is_read, created_at)
			 VALUES (?, ?, ?, 'report', 0, ?);`,
			postAuthorID, "你的文章已处理", "你的文章因被举报，已被管理员隐藏。", time.Now().Unix(),
		); err != nil {
			return fmt.Errorf("写入作者通知失败: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交审核事务失败: %w", err)
	}
	return nil
}

func (s *Store) ListSessionsByUser(userID int64) ([]SessionInfo, error) {
	defer s.logSlow("ListSessionsByUser", time.Now())

	if userID == 0 {
		return nil, errors.New("operation failed")
	}
	rows, err := s.db.Query(
		`SELECT id, user_id, ip, user_agent, created_at, expires_at
		 FROM sessions
		 WHERE user_id = ?
		 ORDER BY created_at DESC, expires_at DESC;`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var list []SessionInfo
	for rows.Next() {
		var item SessionInfo
		var createdAt, expiresAt int64
		if err := rows.Scan(&item.ID, &item.UserID, &item.IP, &item.UserAgent, &createdAt, &expiresAt); err != nil {
			return nil, fmt.Errorf("scan sessions: %w", err)
		}
		item.CreatedAt = unixToTime(createdAt)
		item.ExpiresAt = unixToTime(expiresAt)
		list = append(list, item)
	}
	return list, nil
}

func (s *Store) DeleteSessionByUser(sessionID string, userID int64) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || userID == 0 {
		return errors.New("invalid session parameters")
	}
	_, err := s.db.Exec(`DELETE FROM sessions WHERE id = ? AND user_id = ?;`, sessionID, userID)
	if err != nil {
		return fmt.Errorf("store error: %w", err)
	}
	return nil
}

func (s *Store) UpdateUserBan(id int64, banned bool, reason string) error {
	if id == 0 {
		return errors.New("invalid user id")
	}
	reason = strings.TrimSpace(reason)
	bannedAt := int64(0)
	if banned {
		bannedAt = time.Now().Unix()
		if reason == "" {
			reason = "account was banned by an administrator"
		}
	}
	_, err := s.db.Exec(
		`UPDATE users
		 SET is_banned = ?, banned_reason = ?, banned_at = ?
		 WHERE id = ?;`,
		boolToInt(banned), reason, bannedAt, id,
	)
	if err != nil {
		return fmt.Errorf("update ban status: %w", err)
	}
	return nil
}

func (s *Store) SetUserEmail(userID int64, email string, verified bool) error {
	if userID == 0 {
		return errors.New("operation failed")
	}
	email = strings.TrimSpace(strings.ToLower(email))
	_, err := s.db.Exec(`UPDATE users SET email = ?, email_verified = ? WHERE id = ?;`, email, boolToInt(verified), userID)
	if err != nil {
		return fmt.Errorf("update user email: %w", err)
	}
	return nil
}

func (s *Store) CreatePasswordResetToken(username string, ttl time.Duration) (string, int64, int64, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return "", 0, 0, errors.New("username is required")
	}
	var userID int64
	if err := s.db.QueryRow(`SELECT id FROM users WHERE username = ?;`, username).Scan(&userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", 0, 0, errors.New("user not found")
		}
		return "", 0, 0, fmt.Errorf("load user for password reset: %w", err)
	}
	token, err := randomTokenURLSafe(32)
	if err != nil {
		return "", 0, 0, fmt.Errorf("generate password reset token: %w", err)
	}
	now := time.Now().Unix()
	expiresAt := time.Now().Add(ttl).Unix()
	_, err = s.db.Exec(
		`INSERT INTO password_reset_tokens (token, user_id, expires_at, created_at, used_at)
		 VALUES (?, ?, ?, ?, 0);`,
		token, userID, expiresAt, now,
	)
	if err != nil {
		return "", 0, 0, fmt.Errorf("store password reset token: %w", err)
	}
	return token, userID, expiresAt, nil
}

func (s *Store) ResetPasswordByToken(token, newPassword string) (int64, error) {
	token = strings.TrimSpace(token)
	newPassword = strings.TrimSpace(newPassword)
	if token == "" || newPassword == "" {
		return 0, errors.New("token and new password are required")
	}
	hash, err := hashPasswordArgon2id(newPassword)
	if err != nil {
		return 0, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin password reset transaction: %w", err)
	}
	defer tx.Rollback()

	var (
		userID    int64
		expiresAt int64
		usedAt    int64
	)
	if err := tx.QueryRow(`SELECT user_id, expires_at, used_at FROM password_reset_tokens WHERE token = ?;`, token).Scan(&userID, &expiresAt, &usedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, errors.New("invalid reset token")
		}
		return 0, fmt.Errorf("load password reset token: %w", err)
	}
	if usedAt > 0 {
		return 0, errors.New("reset token has already been used")
	}
	if time.Now().Unix() > expiresAt {
		return 0, errors.New("reset token has expired")
	}

	if _, err := tx.Exec(`UPDATE users SET password_hash = ? WHERE id = ?;`, hash, userID); err != nil {
		return 0, fmt.Errorf("update user password: %w", err)
	}
	if _, err := tx.Exec(`UPDATE password_reset_tokens SET used_at = ? WHERE token = ?;`, time.Now().Unix(), token); err != nil {
		return 0, fmt.Errorf("mark password reset token as used: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM sessions WHERE user_id = ?;`, userID); err != nil {
		return 0, fmt.Errorf("delete user sessions after password reset: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit password reset transaction: %w", err)
	}
	return userID, nil
}

func (s *Store) CreateEmailVerifyToken(userID int64, email string, ttl time.Duration) (string, int64, error) {
	if userID == 0 {
		return "", 0, errors.New("invalid user id")
	}
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" {
		return "", 0, errors.New("email is required")
	}
	token, err := randomTokenURLSafe(32)
	if err != nil {
		return "", 0, fmt.Errorf("generate email verification token: %w", err)
	}
	now := time.Now().Unix()
	expiresAt := time.Now().Add(ttl).Unix()
	_, err = s.db.Exec(
		`INSERT INTO email_verify_tokens (token, user_id, email, expires_at, created_at, used_at)
		 VALUES (?, ?, ?, ?, ?, 0);`,
		token, userID, email, expiresAt, now,
	)
	if err != nil {
		return "", 0, fmt.Errorf("store email verification token: %w", err)
	}
	return token, expiresAt, nil
}

func (s *Store) VerifyEmailByToken(token string) (int64, string, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return 0, "", errors.New("email verification token is required")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, "", fmt.Errorf("begin email verification transaction: %w", err)
	}
	defer tx.Rollback()

	var (
		userID    int64
		email     string
		expiresAt int64
		usedAt    int64
	)
	if err := tx.QueryRow(`SELECT user_id, email, expires_at, used_at FROM email_verify_tokens WHERE token = ?;`, token).Scan(&userID, &email, &expiresAt, &usedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, "", errors.New("invalid email verification token")
		}
		return 0, "", fmt.Errorf("load email verification token: %w", err)
	}
	if usedAt > 0 {
		return 0, "", errors.New("email verification token has already been used")
	}
	if time.Now().Unix() > expiresAt {
		return 0, "", errors.New("email verification token has expired")
	}

	if _, err := tx.Exec(`UPDATE users SET email = ?, email_verified = 1 WHERE id = ?;`, email, userID); err != nil {
		return 0, "", fmt.Errorf("update verified email: %w", err)
	}
	if _, err := tx.Exec(`UPDATE email_verify_tokens SET used_at = ? WHERE token = ?;`, time.Now().Unix(), token); err != nil {
		return 0, "", fmt.Errorf("mark email verification token as used: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, "", fmt.Errorf("commit email verification transaction: %w", err)
	}
	return userID, email, nil
}
