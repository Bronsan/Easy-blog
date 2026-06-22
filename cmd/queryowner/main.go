package main

import (
	"database/sql"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

func main() {
	db, err := sql.Open("sqlite", "data/blog.db")
	if err != nil {
		fmt.Println("打开数据库失败:", err)
		os.Exit(1)
	}
	defer db.Close()

	rows, err := db.Query(`SELECT id, username, display_name, role FROM users WHERE role='owner';`)
	if err != nil {
		fmt.Println("查询失败:", err)
		os.Exit(1)
	}
	defer rows.Close()

	fmt.Println("站长账号列表:")
	for rows.Next() {
		var id int64
		var username, displayName, role string
		_ = rows.Scan(&id, &username, &displayName, &role)
		fmt.Printf("  ID=%d  用户名=%s  显示名=%s  角色=%s\n", id, username, displayName, role)
	}
}
