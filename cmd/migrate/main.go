// 数据库迁移工具：go run ./cmd/migrate [up|down N|status]。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/DuoHuo/nuist-sta-app-backend/internal/config"
	"github.com/DuoHuo/nuist-sta-app-backend/internal/platform/database"
)

func main() {
	var (
		configPath = flag.String("config", "", "YAML 配置文件路径（可空）")
		path       = flag.String("path", "migrations", "迁移文件目录")
	)
	flag.Parse()

	cmd := "up"
	args := flag.Args()
	if len(args) > 0 {
		cmd = args[0]
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := database.Connect(ctx, cfg.Database)
	if err != nil {
		log.Fatalf("连接数据库失败: %v", err)
	}
	defer pool.Close()

	switch cmd {
	case "up":
		n, err := database.MigrateUp(ctx, pool, *path)
		if err != nil {
			log.Fatalf("迁移失败: %v", err)
		}
		fmt.Printf("迁移完成，本次应用 %d 个版本\n", n)
	case "down":
		steps := 1
		if len(args) > 1 {
			if n, err := strconv.Atoi(args[1]); err == nil {
				steps = n
			}
		}
		if err := database.MigrateDown(ctx, pool, *path, steps); err != nil {
			log.Fatalf("回滚失败: %v", err)
		}
		fmt.Printf("已回滚 %d 个版本\n", steps)
	case "status":
		applied, pending, err := database.MigrationStatus(ctx, pool, *path)
		if err != nil {
			log.Fatalf("查询失败: %v", err)
		}
		fmt.Printf("已应用版本: %v\n待应用: %d 个\n", applied, pending)
	default:
		fmt.Fprintln(os.Stderr, "用法: migrate [up|down N|status]")
		os.Exit(2)
	}
}
