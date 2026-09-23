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
	"web2api/internal/auth"
	"web2api/internal/browser"
	"web2api/internal/config"
	"web2api/internal/platform"
	"web2api/internal/router"
	"web2api/pkg/account"
	"web2api/pkg/queue"
)

var version = "dev"

func main() {
	configPath := flag.String("config", "config.toml", "配置文件路径（项目根目录单份 toml）")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	pool := browser.NewPool(cfg)
	stats := router.NewStats()

	// ---- 账号池：每平台按 accounts 配置构建，达 request_limit 自动轮换 ----
	accountPools := map[string]*account.Pool{}
	for _, d := range platform.All() {
		pc, _ := cfg.GetPlatform(d.Name())
		accountPools[d.Name()] = account.New(d.Name(), pc.EffectiveAccounts())
	}
	pool.SetAccountPools(accountPools)

	// ---- 请求队列：容量与 worker 数来自 server.toml ----
	sc := cfg.GetServer()
	if sc.TLSMisconfigured() {
		log.Fatalf("server.tls=true 但未设置 tls_cert / tls_key：请在 config.toml 的 [server] 填写或关闭 tls")
	}
	jobs := queue.New(sc.QueueCapacity, sc.QueueWorkers)

	authCfg := cfg.GetAuth()
	// ---- 管理认证：密码由用户在 auth.toml 手动配置，绝不自动生成 ----
	var pwHash string
	if authCfg.Enabled {
		if authCfg.Password == "" {
			log.Fatalf("auth.enabled=true 但未设置 password：请在 config.toml 的 [auth] password 手动填写")
		}
		pwHash = auth.Hash(authCfg.Password)
	}

	// ---- /v1 API 密钥：由用户在 auth.toml 自行配置，不自动生成 ----
	apiKeys := auth.NewAPIKeyAuth(authCfg.APIKeys)
	if apiKeys.Empty() {
		log.Printf("[提示] 未配置 api_keys：/v1 接口当前**不鉴权**，请在 config.toml 的 [auth] api_keys 里自行填写")
	}

	logs := router.NewLogs(200)
	adm := admin.New(cfg, pool, stats, logs, admin.NewAuth(authCfg.Enabled, authCfg.SessionTTL, pwHash), apiKeys)
	app := router.New(cfg, pool, stats, apiKeys, router.Options{
		Sink:     adm,          // 请求日志实时推送到管理界面 WS
		Queue:    jobs,         // 有界队列，满则 429
		Accounts: accountPools, // 账号用量记账
		Logs:     logs,
	})

	// applyReload：配置变更 / 网页端保存设置后统一重建侧写相关组件。
	applyReload := func() {
		for _, d := range platform.All() {
			if pc, ok := cfg.GetPlatform(d.Name()); ok {
				accountPools[d.Name()] = account.New(d.Name(), pc.EffectiveAccounts())
			}
		}
		pool.SetAccountPools(accountPools)
		pool.ApplyConfig()
		app.Reload()
	}
	// 浏览器常驻：启动即拉起所有启用平台的无头浏览器，请求直接复用。
	go pool.Prestart()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	jobs.Start(ctx)

	mux := http.NewServeMux()
	mux.Handle("/v1/", app.Handler())
	adm.Routes(mux)
	adm.SetReloadHook(applyReload)

	srv := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", sc.Host, sc.Port),
		Handler:           mux,
		ReadHeaderTimeout: 15 * time.Second,
	}

	go pool.KeepAlive(15 * time.Second)
	go cfg.Watch(ctx, 3*time.Second, func() {
		applyReload()
		log.Println("配置已热加载")
	})

	go func() {
		scheme := "http"
		if sc.UseTLS() {
			scheme = "https"
		}
		log.Printf("web2api v%s 启动", version)
		log.Printf("管理界面: %s://%s:%d", scheme, sc.Host, sc.Port)
		log.Printf("API 服务: %s://%s:%d/v1（需 Authorization: Bearer <API Key>）", scheme, sc.Host, sc.Port)
		var err error
		if sc.UseTLS() {
			err = srv.ListenAndServeTLS(sc.TLSCert, sc.TLSKey)
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP 服务异常退出: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("正在关闭服务…")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	jobs.Shutdown(context.Background())
	pool.Close()
	log.Println("已退出")
}