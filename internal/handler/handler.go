package handler

import (
	"encoding/binary"
	"net"
	"strings"
	"time"

	json "github.com/goccy/go-json"

	"github.com/Chocola-X/NekoIPinfo/internal/db"
	"github.com/Chocola-X/NekoIPinfo/internal/logger"
	"github.com/Chocola-X/NekoIPinfo/internal/model"
	"github.com/valyala/bytebufferpool"
	"github.com/valyala/fasthttp"
)

var (
	respInvalidIP  []byte
	respNotFoundMem []byte
	respNotFoundDB []byte
	respQueryError []byte
	respSysError   = []byte(`{"code":500,"msg":"系统内部错误","data":null}`)

	successPrefix = []byte(`{"code":200,"msg":"success","data":{"ip":"`)
	successMiddle = []byte(`",`)
	successSuffix = []byte(`}`)

	specialPrefix = []byte(`{"code":200,"msg":"success","data":{"ip":"`)
	specialSuffix = []byte(`","country":"局域网","province":"","city":"","isp":"Private/Reserved","latitude":"","longitude":""}}`)

	contentTypeJSON = []byte("application/json; charset=utf-8")
	corsKey         = []byte("Access-Control-Allow-Origin")
	corsValue       = []byte("*")
)

func init() {
	respInvalidIP, _ = json.Marshal(model.APIResponse{Code: 400, Msg: "非法的 IP 地址喵！", Data: nil})
	respNotFoundMem, _ = json.Marshal(model.APIResponse{Code: 404, Msg: "内存库里没有找到这个 IP 喵~", Data: nil})
	respNotFoundDB, _ = json.Marshal(model.APIResponse{Code: 404, Msg: "数据库里没有找到这个 IP 喵~", Data: nil})
	respQueryError, _ = json.Marshal(model.APIResponse{Code: 500, Msg: "数据库查询出错了喵", Data: nil})
}

type Handler struct {
	Store  *db.Store
	Logger *logger.AsyncLogger
}

func New(store *db.Store, log *logger.AsyncLogger) *Handler {
	return &Handler{
		Store:  store,
		Logger: log,
	}
}

func (h *Handler) setHeaders(ctx *fasthttp.RequestCtx) {
	ctx.Response.Header.SetContentTypeBytes(contentTypeJSON)
	ctx.Response.Header.SetBytesKV(corsKey, corsValue)
}

func parseIPv4Fast(s string) (uint64, uint64, bool) {
	var parts [4]uint32
	partIdx := 0
	cur := uint32(0)
	digits := 0
	start := 0

	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' {
			if digits == 0 {
				start = i
			}
			cur = cur*10 + uint32(c-'0')
			digits++
			if digits > 3 || cur > 255 {
				return 0, 0, false
			}
		} else if c == '.' {
			if digits == 0 || partIdx >= 3 {
				return 0, 0, false
			}
			if digits > 1 && s[start] == '0' {
				return 0, 0, false
			}
			parts[partIdx] = cur
			partIdx++
			cur = 0
			digits = 0
		} else {
			return 0, 0, false
		}
	}

	if digits == 0 || partIdx != 3 {
		return 0, 0, false
	}
	if digits > 1 && s[start] == '0' {
		return 0, 0, false
	}
	parts[3] = cur

	ipv4 := parts[0]<<24 | parts[1]<<16 | parts[2]<<8 | parts[3]
	lo := uint64(0x0000ffff00000000) | uint64(ipv4)
	return 0, lo, true
}

func parseIPGeneric(s string) (uint64, uint64, bool) {
	ip := net.ParseIP(s)
	if ip == nil {
		return 0, 0, false
	}
	b := ip.To16()
	if b == nil {
		return 0, 0, false
	}
	hi := binary.BigEndian.Uint64(b[:8])
	lo := binary.BigEndian.Uint64(b[8:16])
	return hi, lo, true
}

func isSpecialIP(ipHi, ipLo uint64) bool {
	if ipHi == 0 && (ipLo>>32) == 0x0000ffff {
		ipv4 := uint32(ipLo)
		switch {
		case ipv4>>24 == 0:
			return true
		case ipv4>>24 == 10:
			return true
		case ipv4>>22 == 0x6440:
			return true
		case ipv4>>24 == 127:
			return true
		case ipv4>>16 == 0xa9fe:
			return true
		case ipv4>>20 == 0xac1:
			return true
		case ipv4>>24 == 192 && (ipv4>>16)&0xff == 0:
			return true
		case ipv4>>24 == 192 && (ipv4>>16)&0xff == 2:
			return true
		case ipv4>>16 == 0xc0a8:
			return true
		case ipv4>>23 == 0xc612:
			return true
		case ipv4>>24 == 198 && (ipv4>>16)&0xff == 51:
			return true
		case ipv4>>24 == 203 && (ipv4>>16)&0xff == 113:
			return true
		case ipv4>>28 == 0xe:
			return true
		case ipv4>>28 == 0xf:
			return true
		}
		return false
	}

	if ipHi == 0 && ipLo == 0 {
		return true
	}
	if ipHi == 0 && ipLo == 1 {
		return true
	}
	if ipHi>>57 == 0x7e {
		return true
	}
	if ipHi>>54 == 0x3fa {
		return true
	}
	if ipHi>>56 == 0xff {
		return true
	}
	if ipHi>>32 == 0x20010db8 {
		return true
	}
	if ipHi>>36 == 0x200101 {
		return true
	}
	if ipHi>>48 == 0x2002 {
		return true
	}
	if ipHi>>32 == 0x20010000 {
		return true
	}
	if ipHi == 0x0064ff9b00000000 {
		return true
	}
	if ipHi == 0x0100000000000000 {
		return true
	}
	return false
}

func firstAddr(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			return s[:i]
		}
	}
	return s
}

func (h *Handler) getClientIP(ctx *fasthttp.RequestCtx) string {
	ip := string(ctx.Request.Header.Peek("X-Real-IP"))
	if ip == "" {
		ip = string(ctx.Request.Header.Peek("X-Forwarded-For"))
	}
	if ip == "" {
		addr := ctx.RemoteAddr().String()
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			ip = addr
		} else {
			ip = host
		}
	}
	ip = firstAddr(ip)
	return strings.TrimSpace(ip)
}

type infoFields struct {
	Country  string `json:"country"`
	Province string `json:"province"`
	City     string `json:"city"`
	ISP      string `json:"isp"`
}

func (h *Handler) logAccess(clientIP, targetIP string, code int, infoJSON []byte, latencyUs int64) {
	if h.Logger == nil {
		return
	}

	var country, province, city, isp string
	if len(infoJSON) > 2 {
		var f infoFields
		if json.Unmarshal(infoJSON, &f) == nil {
			country = f.Country
			province = f.Province
			city = f.City
			isp = f.ISP
		}
	}
	h.Logger.Log(clientIP, targetIP, code, country, province, city, isp, latencyUs)
}

func (h *Handler) sendPrebuilt(ctx *fasthttp.RequestCtx, clientIP, targetIP string, code int, data []byte, start time.Time) {
	h.setHeaders(ctx)
	ctx.Response.SetBodyRaw(data)
	h.logAccess(clientIP, targetIP, code, nil, time.Since(start).Microseconds())
}

func buildSuccessBody(targetIP string, infoJSON []byte) []byte {
	if len(infoJSON) < 2 || infoJSON[0] != '{' {
		return respSysError
	}

	buf := bytebufferpool.Get()
	buf.B = append(buf.B[:0], successPrefix...)
	buf.B = append(buf.B, targetIP...)
	buf.B = append(buf.B, successMiddle...)
	buf.B = append(buf.B, infoJSON[1:]...)
	buf.B = append(buf.B, successSuffix...)
	result := make([]byte, len(buf.B))
	copy(result, buf.B)
	bytebufferpool.Put(buf)

	return result
}

func (h *Handler) sendSuccess(ctx *fasthttp.RequestCtx, clientIP, targetIP string, infoJSON []byte, start time.Time) {
	h.setHeaders(ctx)
	body := buildSuccessBody(targetIP, infoJSON)
	ctx.Response.SetBodyRaw(body)
	h.logAccess(clientIP, targetIP, 200, infoJSON, time.Since(start).Microseconds())
}

func (h *Handler) sendSpecial(ctx *fasthttp.RequestCtx, clientIP, targetIP string, start time.Time) {
	h.setHeaders(ctx)

	buf := bytebufferpool.Get()
	buf.B = append(buf.B[:0], specialPrefix...)
	buf.B = append(buf.B, targetIP...)
	buf.B = append(buf.B, specialSuffix...)
	body := make([]byte, len(buf.B))
	copy(body, buf.B)
	bytebufferpool.Put(buf)

	ctx.Response.SetBodyRaw(body)
	h.logAccess(clientIP, targetIP, 200, []byte(`{"country":"局域网","isp":"Private/Reserved"}`), time.Since(start).Microseconds())
}

func (h *Handler) IPInfoHandler(ctx *fasthttp.RequestCtx) {
	start := time.Now()

	clientIP := h.getClientIP(ctx)

	queryIPBytes := ctx.QueryArgs().Peek("ip")
	targetIP := strings.TrimSpace(string(queryIPBytes))
	if targetIP == "" {
		targetIP = clientIP
	}

	ipHi, ipLo, ok := parseIPv4Fast(targetIP)
	if !ok {
		ipHi, ipLo, ok = parseIPGeneric(targetIP)
		if !ok {
			h.sendPrebuilt(ctx, clientIP, targetIP, 400, respInvalidIP, start)
			return
		}
	}

	if isSpecialIP(ipHi, ipLo) {
		h.sendSpecial(ctx, clientIP, targetIP, start)
		return
	}

	switch h.Store.MemMode {
	case db.MemModeFull:
		infoJSON, found := h.Store.LookupFull(ipHi, ipLo)
		if found {
			h.sendSuccess(ctx, clientIP, targetIP, infoJSON, start)
			return
		}
		h.sendPrebuilt(ctx, clientIP, targetIP, 404, respNotFoundMem, start)

	case db.MemModeFast:
		infoJSON, found := h.Store.LookupFast(ipHi, ipLo)
		if found {
			h.sendSuccess(ctx, clientIP, targetIP, infoJSON, start)
			return
		}
		h.sendPrebuilt(ctx, clientIP, targetIP, 404, respNotFoundMem, start)

	default:
		infoJSON, err := h.Store.LookupPebble(ipHi, ipLo)
		if err != nil {
			h.sendPrebuilt(ctx, clientIP, targetIP, 500, respQueryError, start)
			return
		}
		if infoJSON == nil {
			h.sendPrebuilt(ctx, clientIP, targetIP, 404, respNotFoundDB, start)
			return
		}
		h.sendSuccess(ctx, clientIP, targetIP, infoJSON, start)
	}
}