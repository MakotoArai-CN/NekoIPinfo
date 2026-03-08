package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

var db *sql.DB

// --- 全局配置与缓存 ---
var (
	ipCache         []IPRule // 全量内存切片
	useMemoryCache  bool     // 内存模式开关
	enableDetailLog bool     // 详细日志开关
)

type IPRule struct {
	NetworkStart uint32
	NetworkEnd   uint32
	Info         IPInfo
}

type IPInfo struct {
	IP        string `json:"ip"`
	Country   string `json:"country"`
	Province  string `json:"province"`
	City      string `json:"city"`
	ISP       string `json:"isp"`
	Latitude  string `json:"latitude"`
	Longitude string `json:"longitude"`
}

type APIResponse struct {
	Code int         `json:"code"`
	Msg  string      `json:"msg"`
	Data interface{} `json:"data"`
}

// 辅助函数：获取客户端真实 IP
func getClientIP(r *http.Request) string {
	ip := r.Header.Get("X-Real-IP")
	if ip == "" {
		ip = r.Header.Get("X-Forwarded-For")
	}
	if ip == "" {
		ip, _, _ = net.SplitHostPort(r.RemoteAddr)
	}
	ip = strings.Split(ip, ",")[0]
	return strings.TrimSpace(ip)
}

// 统一处理响应和日志打印
func sendJSONResponse(w http.ResponseWriter, clientIP, targetIP string, resp APIResponse) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// 将结构体序列化为 JSON 字节
	respBytes, err := json.Marshal(resp)
	if err != nil {
		log.Printf("响应序列化失败: %v", err)
		http.Error(w, `{"code":500,"msg":"系统内部错误","data":null}`, http.StatusInternalServerError)
		return
	}

	// 如果开启了日志，就在这里统一输出
	if enableDetailLog {
		log.Printf("[访问日志] 来源IP: %-15s | 查询IP: %-15s | 结果: %s", clientIP, targetIP, string(respBytes))
	}

	// 写入响应
	w.Write(respBytes)
}

func loadDataToMemory() error {
	log.Println("正在将数据库载入内存，请稍候喵...")
	startTime := time.Now()

	rows, err := db.Query(`SELECT network_start, network_end, ip_info_json FROM ip_info ORDER BY network_start ASC`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var start, end uint32
		var infoJSON string
		if err := rows.Scan(&start, &end, &infoJSON); err != nil {
			return err
		}

		var rawData map[string]string
		if err := json.Unmarshal([]byte(infoJSON), &rawData); err != nil {
			continue
		}

		info := IPInfo{
			Country:   rawData["country"],
			Province:  rawData["province"],
			City:      rawData["city"],
			ISP:       rawData["isp"],
			Latitude:  rawData["latitude"],
			Longitude: rawData["longitude"],
		}

		ipCache = append(ipCache, IPRule{
			NetworkStart: start,
			NetworkEnd:   end,
			Info:         info,
		})
	}

	log.Printf("载入完成！共加载了 %d 条规则，耗时 %v 喵！", len(ipCache), time.Since(startTime))
	return nil
}

func apiHandler(w http.ResponseWriter, r *http.Request) {
	clientIP := getClientIP(r) // 提取实际访问者的 IP，用于日志记录

	queryIPStr := strings.TrimSpace(r.URL.Query().Get("ip"))
	targetIP := queryIPStr
	if targetIP == "" {
		targetIP = clientIP
	}

	parsedIP := net.ParseIP(targetIP)
	if parsedIP == nil || parsedIP.To4() == nil {
		sendJSONResponse(w, clientIP, targetIP, APIResponse{Code: 400, Msg: "非法的 IPv4 地址喵！", Data: nil})
		return
	}

	ipInt := uint32(parsedIP.To4()[0])<<24 | uint32(parsedIP.To4()[1])<<16 | uint32(parsedIP.To4()[2])<<8 | uint32(parsedIP.To4()[3])

	if useMemoryCache {
		idx := sort.Search(len(ipCache), func(i int) bool {
			return ipCache[i].NetworkStart > ipInt
		})

		if idx > 0 {
			rule := ipCache[idx-1]
			if ipInt <= rule.NetworkEnd {
				result := rule.Info
				result.IP = targetIP
				sendJSONResponse(w, clientIP, targetIP, APIResponse{Code: 200, Msg: "success", Data: result})
				return
			}
		}

		sendJSONResponse(w, clientIP, targetIP, APIResponse{Code: 404, Msg: "内存库里没有找到这个 IP 喵~", Data: nil})

	} else {
		var infoJSON string
		var networkEnd uint32

		err := db.QueryRow(`
		SELECT network_end, ip_info_json
		FROM ip_info
		WHERE network_start <= ?
		ORDER BY network_start DESC
		LIMIT 1`, ipInt).Scan(&networkEnd, &infoJSON)

		if err != nil {
			if err == sql.ErrNoRows {
				sendJSONResponse(w, clientIP, targetIP, APIResponse{Code: 404, Msg: "数据库里没有找到这个 IP 喵~", Data: nil})
			} else {
				log.Printf("数据库查询错误: %v\n", err)
				sendJSONResponse(w, clientIP, targetIP, APIResponse{Code: 500, Msg: "数据库查询出错了喵", Data: nil})
			}
			return
		}

		if ipInt > networkEnd {
			sendJSONResponse(w, clientIP, targetIP, APIResponse{Code: 404, Msg: "数据库里没有找到这个 IP 喵~", Data: nil})
			return
		}

		var rawData map[string]string
		if err := json.Unmarshal([]byte(infoJSON), &rawData); err != nil {
			log.Printf("JSON 解析错误: %v\n", err)
			sendJSONResponse(w, clientIP, targetIP, APIResponse{Code: 500, Msg: "数据解析失败喵", Data: nil})
			return
		}

		result := IPInfo{
			IP:        targetIP,
			Country:   rawData["country"],
			Province:  rawData["province"],
			City:      rawData["city"],
			ISP:       rawData["isp"],
			Latitude:  rawData["latitude"],
			Longitude: rawData["longitude"],
		}

		sendJSONResponse(w, clientIP, targetIP, APIResponse{Code: 200, Msg: "success", Data: result})
	}
}

func main() {
	dbPath := flag.String("db", "ip_info.db", "SQLite 数据库文件路径")
	port := flag.String("port", "8080", "API 服务监听端口")
	memFlag := flag.Bool("mem", false, "是否开启全量内存模式（内存换取极致性能喵~）")
	logFlag := flag.Bool("log", false, "是否开启详细访问日志输出")

	flag.Parse()
	useMemoryCache = *memFlag
	enableDetailLog = *logFlag // 赋值给全局变量

	var err error
	db, err = sql.Open("sqlite3", *dbPath)
	if err != nil {
		log.Fatal("无法打开数据库: ", err)
	}
	defer db.Close()

	if useMemoryCache {
		if err := loadDataToMemory(); err != nil {
			log.Fatal("致命错误：无法将数据加载到内存喵: ", err)
		}
	}

	http.HandleFunc("/ipinfo", apiHandler)

	addr := fmt.Sprintf(":%s", *port)
	fmt.Printf("猫娘 API 服务启动于 %s 喵...\n", addr)
	fmt.Printf("当前数据库文件: %s\n", *dbPath)

	if useMemoryCache {
		fmt.Println("当前运行模式: [极致性能] 全量内存 + 二分查找")
	} else {
		fmt.Println("当前运行模式: [省内存] SQLite 实时查询 (可加 -mem=true 提速，将数据库写入内存，开启前请确保内存充足)")
	}

	if enableDetailLog {
		fmt.Println("详细访问日志: 已开启 (将在控制台打印每次请求细节)")
	} else {
		fmt.Println("详细访问日志: 未开启 (可加 -log=true 开启)")
	}

	log.Fatal(http.ListenAndServe(addr, nil))
}
