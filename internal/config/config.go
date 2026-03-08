package config

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Port         string
	DBPath       string
	MemMode      string
	LogDays      int
	LogPath      string
	EnableStatic bool
	StaticDir    string
	DisableColor bool
	MaxMem       string
	MaxCPU       string
	ShowVersion  bool
	UpdateTarget string
	DoUpdate     bool
}

func Parse() *Config {
	cfg := &Config{}

	flag.StringVar(&cfg.Port, "port", "8080", "API 服务监听端口")
	flag.StringVar(&cfg.DBPath, "db", "ip_info", "Pebble 数据库路径")
	flag.StringVar(&cfg.MemMode, "mem", "off", "内存模式: off=纯Pebble, fast=LRU缓存加速, full=全量内存")
	flag.StringVar(&cfg.LogPath, "logdir", "ip_info_log", "日志数据库存储目录")
	flag.BoolVar(&cfg.EnableStatic, "static", false, "启用内嵌静态文件服务")
	flag.StringVar(&cfg.StaticDir, "staticdir", "static", "静态文件目录路径")
	flag.BoolVar(&cfg.DisableColor, "no-color", false, "禁用终端彩色输出")
	flag.StringVar(&cfg.MaxMem, "max-mem", "", "最大内存限制 (例: 512M, 1G, 50%)")
	flag.StringVar(&cfg.MaxCPU, "max-cpu", "", "最大CPU限制 (例: 2, 50%)")
	flag.BoolVar(&cfg.ShowVersion, "v", false, "显示版本信息")

	logDaysStr := ""
	flag.Func("log", "日志保留天数: -1=不记录, 空/true=仅控制台, 0=永久存储, N=保留N天", func(s string) error {
		logDaysStr = s
		return nil
	})

	updateStr := ""
	flag.Func("update", "检查并更新: 无参数=自动更新, 指定版本号=强制更新到该版本", func(s string) error {
		updateStr = s
		return nil
	})

	flag.Parse()

	for _, arg := range os.Args[1:] {
		if arg == "-version" || arg == "--version" {
			cfg.ShowVersion = true
		}
	}

	for _, arg := range os.Args[1:] {
		a := strings.TrimLeft(arg, "-")
		if a == "update" || strings.HasPrefix(a, "update=") {
			cfg.DoUpdate = true
			break
		}
	}

	if cfg.DoUpdate {
		cfg.UpdateTarget = updateStr
	}

	cfg.LogDays = -2
	if logDaysStr == "" {
		found := false
		for _, arg := range os.Args[1:] {
			if arg == "-log" || arg == "--log" || arg == "-log=true" || arg == "--log=true" {
				found = true
				break
			}
		}
		if found && logDaysStr == "" {
			cfg.LogDays = -1
		}
	} else {
		switch logDaysStr {
		case "true":
			cfg.LogDays = -1
		case "false":
			cfg.LogDays = -2
		default:
			n, err := strconv.Atoi(logDaysStr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "无效的 -log 参数: %s\n", logDaysStr)
				os.Exit(1)
			}
			cfg.LogDays = n
		}
	}

	memFound := false
	for _, arg := range os.Args[1:] {
		if arg == "-mem" || arg == "--mem" {
			memFound = true
			break
		}
	}
	if memFound && cfg.MemMode == "off" {
		cfg.MemMode = "full"
	}

	return cfg
}