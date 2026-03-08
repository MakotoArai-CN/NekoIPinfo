package main

import (
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/Chocola-X/NekoIPinfo/internal/config"
	"github.com/Chocola-X/NekoIPinfo/internal/db"
	"github.com/Chocola-X/NekoIPinfo/internal/dbgen"
	"github.com/Chocola-X/NekoIPinfo/internal/handler"
	"github.com/Chocola-X/NekoIPinfo/internal/logger"
	"github.com/Chocola-X/NekoIPinfo/internal/resource"
	"github.com/Chocola-X/NekoIPinfo/internal/sysinfo"
	"github.com/Chocola-X/NekoIPinfo/internal/version"
	"github.com/fasthttp/router"
	"github.com/valyala/fasthttp"
)

func main() {
	cpus := runtime.NumCPU()
	runtime.GOMAXPROCS(cpus)

	cfg := config.Parse()

	if cfg.DisableColor {
		dbgen.SetColorEnabled(false)
	}

	if cfg.ShowVersion {
		version.PrintVersion()
		os.Exit(0)
	}

	if cfg.DoUpdate {
		version.DoUpdate(cfg.UpdateTarget)
		return
	}

	memMode := cfg.MemMode
	switch memMode {
	case db.MemModeOff, db.MemModeFast, db.MemModeFull:
	default:
		dbgen.NekoError(fmt.Sprintf("无效的 -mem 参数: %s（可选: off, fast, full）", memMode))
		os.Exit(1)
	}

	limits := resource.Parse(cfg.MaxMem, cfg.MaxCPU)
	if limits.IsEnabled() {
		limits.Apply()
	}

	dbgen.NekoHeader("NekoIPinfo 服务启动")
	fmt.Println()

	dbgen.NekoKV("版本", "v"+version.GetCurrent())

	si := sysinfo.Collect()
	dbgen.NekoKV("CPU", fmt.Sprintf("%s (%d核 %d线程)", si.CPUModelName, si.CPUCores, si.CPUThreads))
	dbgen.NekoKV("系统内存", fmt.Sprintf("总计 %d MB / 可用 %d MB", si.TotalMemMB, si.AvailMemMB))
	dbgen.NekoKV("数据库路径", cfg.DBPath)
	dbgen.NekoKV("数据库大小", fmt.Sprintf("%.1f MB", sysinfo.DatabaseSizeMB(cfg.DBPath)))

	if limits.IsEnabled() {
		dbgen.NekoKV("资源限制", limits.Summary())
	}

	fmt.Println()

	store, err := db.Open(cfg.DBPath, memMode, limits.MemoryBytes)
	if err != nil {
		dbgen.NekoError(fmt.Sprintf("无法打开数据库: %v", err))
		os.Exit(1)
	}
	defer store.Close()

	actualMode := store.MemMode
	switch actualMode {
	case db.MemModeFull:
		dbgen.NekoKV("运行模式", "🚀 极致性能 - 全量内存 + 二分查找")
	case db.MemModeFast:
		dbgen.NekoKV("运行模式", "⚡ 高性能 - 索引驻内存 + LRU 缓存")
	default:
		dbgen.NekoKV("运行模式", "♻ 低内存 - Pebble KV 实时查询")
		dbgen.Neko(" 可加 -mem 提速（默认 full 模式），或 -mem=fast 获得均衡性能", dbgen.ColorDim)
	}

	var asyncLog *logger.AsyncLogger
	if cfg.LogDays >= -1 {
		asyncLog, err = logger.New(cfg.LogPath, cfg.LogDays)
		if err != nil {
			dbgen.NekoError(fmt.Sprintf("无法创建日志: %v", err))
			os.Exit(1)
		}
		if asyncLog != nil {
			defer asyncLog.Close()
		}
	}

	switch {
	case cfg.LogDays == -2:
		dbgen.NekoKV("访问日志", "未开启（可加 -log 开启控制台输出）")
	case cfg.LogDays == -1:
		dbgen.NekoKV("访问日志", "仅控制台输出")
	case cfg.LogDays == 0:
		dbgen.NekoKV("访问日志", fmt.Sprintf("持久化存储（永久保留）-> %s", cfg.LogPath))
	default:
		dbgen.NekoKV("访问日志", fmt.Sprintf("持久化存储（保留 %d 天）-> %s", cfg.LogDays, cfg.LogPath))
	}

	h := handler.New(store, asyncLog)

	r := router.New()
	r.GET("/ipinfo", h.IPInfoHandler)

	var finalHandler fasthttp.RequestHandler
	if cfg.EnableStatic {
		dbgen.NekoKV("静态文件", fmt.Sprintf("已启用 -> %s", cfg.StaticDir))

		fs := &fasthttp.FS{
			Root:               cfg.StaticDir,
			IndexNames:         []string{"index.html"},
			GenerateIndexPages: false,
			Compress:           true,
			CompressBrotli:     true,
			CacheDuration:      10 * time.Minute,
		}
		fsHandler := fs.NewRequestHandler()

		finalHandler = func(ctx *fasthttp.RequestCtx) {
			path := string(ctx.Path())
			if path == "/ipinfo" {
				r.Handler(ctx)
				return
			}
			fsHandler(ctx)
		}
	} else {
		finalHandler = r.Handler
	}

	serverConcurrency := limits.ServerConcurrency(cpus)
	if !limits.IsEnabled() && serverConcurrency < 65536 {
		serverConcurrency = 65536
	}

	reduceMemory := limits.IsEnabled() && limits.MemoryBytes > 0

	addr := fmt.Sprintf(":%s", cfg.Port)

	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	dbgen.NekoKV("内存占用", fmt.Sprintf("%.1f MB（系统分配 %.1f MB）",
		float64(m.Alloc)/1024/1024, float64(m.Sys)/1024/1024))
	fmt.Println()

	dbgen.NekoSuccess(fmt.Sprintf("API 服务已启动于 %s 喵！", addr))
	dbgen.NekoKV("最大并发连接", fmt.Sprintf("%d", serverConcurrency))
	if reduceMemory {
		dbgen.NekoKV("内存优化", "已启用 ReduceMemoryUsage")
	}
	dbgen.NekoFooter()

	stopCh := make(chan struct{})
	defer close(stopCh)

	if limits.IsEnabled() {
		limits.StartMonitor(stopCh)
	}

	server := &fasthttp.Server{
		Handler:                      finalHandler,
		Name:                         "NekoIPinfo",
		MaxRequestBodySize:           512 * 1024,
		Concurrency:                  serverConcurrency,
		TCPKeepalive:                 true,
		ReduceMemoryUsage:            reduceMemory,
		DisableHeaderNamesNormalizing: true,
		NoDefaultServerHeader:        true,
		NoDefaultDate:                true,
		NoDefaultContentType:         true,
	}

	go func() {
		var ms runtime.MemStats
		for {
			time.Sleep(3 * time.Second)
			runtime.ReadMemStats(&ms)
			fmt.Fprintf(os.Stderr, "\r%s 🐾 内存: %.1f MB / %.1f MB | GC: %d | Goroutines: %d %s",
				dbgen.ColorPink,
				float64(ms.Alloc)/1024/1024,
				float64(ms.Sys)/1024/1024,
				ms.NumGC,
				runtime.NumGoroutine(),
				dbgen.ColorReset)
		}
	}()

	if err := server.ListenAndServe(addr); err != nil {
		dbgen.NekoError(fmt.Sprintf("服务启动失败: %v", err))
		os.Exit(1)
	}
}