package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"web2api/internal/admin"
	"web2api/internal/browser"
	"web2api/internal/config"
	"web2api/internal/router"
)

var version = "dev"

func main() {
	configPath := flag.String("config", "config.yaml", "配置文件路径")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	pool := browser.NewPool(cfg)
	stats := router.NewStats()
	app := router.New(cfg, pool, stats)

	sc := cfg.GetServer()
	mux := http.NewServeMux()
	mux.Handle("/v1/", app.Handler())

	adm := admin.New(cfg, pool, stats)
	adm.Register(mux, sc.AdminPath)

	srv := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", sc.Host, sc.Port),
		Handler:           mux,
		ReadHeaderTimeout: 15 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go pool.KeepAlive(15 * time.Second)
	go cfg.Watch(ctx, 3*time.Second, func() {
		pool.ApplyConfig()
		log.Println("配置已热加载")
	})

	go func() {
		log.Printf("web2api v%s 启动", version)
		log.Printf("管理界面: http://%s:%d%s", sc.Host, sc.Port, sc.AdminPath)
		log.Printf("API 服务: http://%s:%d/v1", sc.Host, sc.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP 服务异常退出: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("正在关闭服务…")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	pool.Close()
	log.Println("已退出")
}
