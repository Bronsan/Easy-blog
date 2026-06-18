package main

import (
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"blog/internal/app"
	"blog/internal/data"
	"blog/internal/view"
)

// main 是应用入口，负责：读取配置、初始化数据库、加载模板与启动 HTTP 服务。
func main() {
	addr := flag.String("addr", ":8080", "HTTP 监听地址")
	dbPath := flag.String("db", "data/blog.db", "SQLite 数据库路径")
	templatesDir := flag.String("templates", "web/templates", "模板目录")
	staticDir := flag.String("static", "web/static", "静态资源目录")
	flag.Parse()

	adminUser := getenvDefault("BLOG_ADMIN_USER", "admin")
	adminPass := strings.TrimSpace(os.Getenv("BLOG_ADMIN_PASS"))
	passGenerated := false
	if adminPass == "" {
		adminPass = mustGenerateBootstrapPassword()
		passGenerated = true
	}
	adminName := getenvDefault("BLOG_ADMIN_NAME", "管理员")

	absDB, err := filepath.Abs(*dbPath)
	if err != nil {
		log.Fatalf("解析数据库路径失败: %v", err)
	}

	store, err := data.Open(absDB)
	if err != nil {
		log.Fatalf("初始化数据库失败: %v", err)
	}
	defer store.Close()

	created, err := store.EnsureAdmin(adminUser, adminPass, adminName)
	if err != nil {
		log.Fatalf("初始化管理员失败: %v", err)
	}
	if created {
		log.Printf("已创建管理员账号: %s", adminUser)
		if passGenerated {
			log.Printf("首次启动自动生成管理员密码: %s", adminPass)
		} else {
			log.Printf("管理员密码来自环境变量 BLOG_ADMIN_PASS")
		}
	}

	if err := store.SeedDefaults(); err != nil {
		log.Fatalf("初始化默认内容失败: %v", err)
	}

	templates, err := view.LoadTemplates(*templatesDir)
	if err != nil {
		log.Fatalf("加载模板失败: %v", err)
	}

	site := app.SiteConfig{
		Title:    "Go SQLite Blog",
		Subtitle: "一个轻量的多用户博客示例",
		MottoCN:  "记录想法，持续创作。",
		MottoEN:  "Write clearly. Publish simply.",
		ICP:      "",
		Gongan:   "",
		Footer:   "Powered by Go + SQLite",
	}
	application := app.New(app.Config{
		Store:      store,
		Templates:  templates,
		StaticDir:  *staticDir,
		Site:       site,
		SessionTTL: 12 * time.Hour,
		AdminUser:  adminUser,
		AdminPass:  adminPass,
		AdminName:  adminName,
	})

	server := &http.Server{
		Addr:         *addr,
		Handler:      application.Routes(),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	fmt.Printf("博客已启动: http://localhost%s\n", *addr)
	fmt.Printf("后台地址: http://localhost%s/admin/login\n", *addr)
	log.Fatal(server.ListenAndServe())
}

// getenvDefault 读取环境变量，缺失时返回默认值。
func getenvDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func mustGenerateBootstrapPassword() string {
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		log.Fatalf("生成初始管理员密码失败: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}
