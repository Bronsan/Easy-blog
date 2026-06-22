package main

import (
	"database/sql"
	"fmt"
	"os"

	"blog/internal/data"

	_ "modernc.org/sqlite"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Println("用法: resetpass <用户名> <新密码>")
		os.Exit(1)
	}
	username := os.Args[1]
	newPassword := os.Args[2]

	db, err := sql.Open("sqlite", "data/blog.db")
	if err != nil {
		fmt.Println("打开数据库失败:", err)
		os.Exit(1)
	}
	defer db.Close()

	hash, err := data.HashPasswordForReset(newPassword)
	if err != nil {
		fmt.Println("生成哈希失败:", err)
		os.Exit(1)
	}

	res, err := db.Exec(`UPDATE users SET password_hash = ? WHERE username = ?;`, hash, username)
	if err != nil {
		fmt.Println("更新失败:", err)
		os.Exit(1)
	}
	affected, _ := res.RowsAffected()
	fmt.Printf("已重置 %s 的密码，影响行数: %d\n", username, affected)
	fmt.Printf("新密码: %s\n", newPassword)
}
