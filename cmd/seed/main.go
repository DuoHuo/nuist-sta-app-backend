// 写入演示数据（幂等）：一栋示范教学楼 + 室内外路网 + 指纹样本。
package main

import (
	"context"
	"flag"
	"log"
	"os/signal"
	"syscall"

	"github.com/DuoHuo/nuist-sta-app-backend/internal/config"
	"github.com/DuoHuo/nuist-sta-app-backend/internal/platform/database"
	"github.com/DuoHuo/nuist-sta-app-backend/internal/seed"
)

func main() {
	var configPath = flag.String("config", "", "YAML 配置文件路径（可空）")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := database.Connect(ctx, cfg.Database)
	if err != nil {
		log.Fatalf("连接数据库失败: %v", err)
	}
	defer pool.Close()

	s, err := seed.Run(ctx, pool)
	if err != nil {
		log.Fatalf("写入演示数据失败: %v", err)
	}
	log.Printf("完成: 建筑=%s 楼层=%v 节点=%d 边=%d POI=%d 指纹会话=%d",
		s.BuildingID, s.FloorIDs, s.NodeCount, s.EdgeCount, s.POICount, s.FPSessions)
}
