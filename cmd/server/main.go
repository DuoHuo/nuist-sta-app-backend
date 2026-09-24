// 地图后端 HTTP 服务入口。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/DuoHuo/nuist-sta-app-backend/internal/config"
	"github.com/DuoHuo/nuist-sta-app-backend/internal/platform/admintoken"
	"github.com/DuoHuo/nuist-sta-app-backend/internal/platform/database"
	"github.com/DuoHuo/nuist-sta-app-backend/internal/seed"
	"github.com/DuoHuo/nuist-sta-app-backend/internal/server"
)

func main() {
	var (
		configPath     = flag.String("config", "", "YAML 配置文件路径（可空，使用默认值 + 环境变量）")
		addrOverride   = flag.String("addr", "", "覆盖监听地址")
		migrationsPath = flag.String("migrations-path", "migrations", "迁移文件目录")
		autoMigrate    = flag.Bool("auto-migrate", false, "启动前执行未应用的迁移")
		seedDemo       = flag.Bool("seed-demo", false, "写入演示数据（幂等，正式部署不要开启）")
	)
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	if *addrOverride != "" {
		cfg.Server.Addr = *addrOverride
	}

	// 管理令牌的启动守卫：格式写错当场退出，没配置就明确告知写接口已关闭。
	// 曾经的实现是"令牌为空就放行"，公网部署等于不设防。
	adminTokens := admintoken.FromSpec(cfg.Server.CollectToken)
	if err := adminTokens.Err(); err != nil {
		log.Fatalf("管理令牌配置有误: %v", err)
	}
	if adminTokens.Configured() {
		log.Printf("管理写接口已启用（%d 个令牌）", adminTokens.Count())
	} else {
		log.Printf("警告: 未配置 server.collect_token（CAMPUS_COLLECT_TOKEN），所有管理写接口将返回 503 并拒绝服务")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := database.Connect(ctx, cfg.Database)
	if err != nil {
		log.Fatalf("连接数据库失败: %v", err)
	}
	defer pool.Close()

	if *autoMigrate {
		n, err := database.MigrateUp(ctx, pool, *migrationsPath)
		if err != nil {
			log.Fatalf("迁移失败: %v", err)
		}
		log.Printf("迁移完成：本次应用 %d 个版本", n)
	}
	if err := database.CheckExtensions(ctx, pool); err != nil {
		log.Printf("警告: %v", err)
	}
	if *seedDemo {
		s, err := seed.Run(ctx, pool)
		if err != nil {
			log.Fatalf("写入演示数据失败: %v", err)
		}
		log.Printf("演示数据就绪: 建筑=%s 节点=%d 边=%d POI=%d 指纹=%d 公交线路=%d 公交站=%d",
			s.BuildingID, s.NodeCount, s.EdgeCount, s.POICount, s.FPSessions,
			s.BusRouteID, s.BusStopCount)
	}

	srv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           server.New(cfg, pool),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("地图后端监听 %s", cfg.Server.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP 服务异常退出: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("收到退出信号，正在关闭…")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		fmt.Fprintln(os.Stderr, "关闭时出错:", err)
	}
	log.Println("已退出")
}
