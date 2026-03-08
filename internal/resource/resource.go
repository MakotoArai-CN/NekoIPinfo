package resource

import (
	"fmt"
	"log"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/Chocola-X/NekoIPinfo/internal/sysinfo"
)

type Limits struct {
	MemoryBytes int64
	CPUCount    int
	enabled     bool
}

func Parse(memStr, cpuStr string) *Limits {
	l := &Limits{}
	if memStr != "" {
		l.MemoryBytes = parseMemory(memStr)
	}
	if cpuStr != "" {
		l.CPUCount = parseCPU(cpuStr)
	}
	l.enabled = l.MemoryBytes > 0 || l.CPUCount > 0
	return l
}

func (l *Limits) IsEnabled() bool {
	return l.enabled
}

func parseMemory(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}

	totalMB := int64(sysinfo.TotalMemoryMB())
	if totalMB <= 0 {
		totalMB = 1024
	}
	totalBytes := totalMB * 1024 * 1024

	if strings.HasSuffix(s, "%") {
		pctStr := strings.TrimSuffix(s, "%")
		pct, err := strconv.ParseFloat(pctStr, 64)
		if err != nil || pct <= 0 || pct > 100 {
			log.Printf("无效的内存百分比: %s，忽略内存限制", s)
			return 0
		}
		result := int64(float64(totalBytes) * pct / 100)
		if result > totalBytes {
			log.Printf("计算出的内存限制 %d MB 超过系统总内存 %d MB，忽略内存限制", result/(1024*1024), totalMB)
			return 0
		}
		return result
	}

	multiplier := int64(1024 * 1024)
	upper := strings.ToUpper(s)
	numStr := s

	if strings.HasSuffix(upper, "GB") {
		numStr = strings.TrimSuffix(upper, "GB")
		multiplier = 1024 * 1024 * 1024
	} else if strings.HasSuffix(upper, "G") {
		numStr = strings.TrimSuffix(upper, "G")
		multiplier = 1024 * 1024 * 1024
	} else if strings.HasSuffix(upper, "MB") {
		numStr = strings.TrimSuffix(upper, "MB")
		multiplier = 1024 * 1024
	} else if strings.HasSuffix(upper, "M") {
		numStr = strings.TrimSuffix(upper, "M")
		multiplier = 1024 * 1024
	} else if strings.HasSuffix(upper, "KB") {
		numStr = strings.TrimSuffix(upper, "KB")
		multiplier = 1024
	} else if strings.HasSuffix(upper, "K") {
		numStr = strings.TrimSuffix(upper, "K")
		multiplier = 1024
	}

	numStr = strings.TrimSpace(numStr)
	val, err := strconv.ParseFloat(numStr, 64)
	if err != nil || val <= 0 {
		log.Printf("无效的内存值: %s，忽略内存限制", s)
		return 0
	}

	result := int64(val * float64(multiplier))
	if result > totalBytes {
		log.Printf("设置的内存限制 %d MB 超过系统总内存 %d MB，忽略内存限制", result/(1024*1024), totalMB)
		return 0
	}
	return result
}

func parseCPU(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}

	maxCPU := runtime.NumCPU()

	if strings.HasSuffix(s, "%") {
		pctStr := strings.TrimSuffix(s, "%")
		pct, err := strconv.ParseFloat(pctStr, 64)
		if err != nil || pct <= 0 || pct > 100 {
			log.Printf("无效的CPU百分比: %s，忽略CPU限制", s)
			return 0
		}
		count := int(float64(maxCPU) * pct / 100)
		if count < 1 {
			count = 1
		}
		if count > maxCPU {
			count = maxCPU
		}
		return count
	}

	val, err := strconv.ParseFloat(s, 64)
	if err != nil || val <= 0 {
		log.Printf("无效的CPU值: %s，忽略CPU限制", s)
		return 0
	}

	count := int(val)
	if count < 1 {
		count = 1
	}

	if maxCPU == 1 {
		log.Printf("单核CPU不支持核心数限制(设置了%d)，忽略CPU限制", count)
		return 0
	}

	if count > maxCPU {
		log.Printf("设置的CPU核心数 %d 超过系统核心数 %d，忽略CPU限制", count, maxCPU)
		return 0
	}
	return count
}

func (l *Limits) Apply() {
	if !l.enabled {
		return
	}

	if l.CPUCount > 0 {
		runtime.GOMAXPROCS(l.CPUCount)
		log.Printf("CPU 限制: %d 核心 (系统 %d 核心)", l.CPUCount, runtime.NumCPU())
	}

	if l.MemoryBytes > 0 {
		softLimit := int64(float64(l.MemoryBytes) * 0.85)
		debug.SetMemoryLimit(softLimit)
		log.Printf("内存限制: %d MB (GC软限制 %d MB)", l.MemoryBytes/(1024*1024), softLimit/(1024*1024))
	}
}

func (l *Limits) StartMonitor(stopCh <-chan struct{}) {
	if !l.enabled || l.MemoryBytes <= 0 {
		return
	}

	go l.memoryMonitorLoop(stopCh)
}

func (l *Limits) memoryMonitorLoop(stopCh <-chan struct{}) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	warningThreshold := int64(float64(l.MemoryBytes) * 0.80)
	criticalThreshold := int64(float64(l.MemoryBytes) * 0.90)

	var consecutiveIneffective int
	var lastGCTime int64
	var lastLogTime int64

	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			alloc := int64(ms.Alloc)

			if alloc > criticalThreshold {
				now := time.Now().UnixMilli()

				cooldown := int64(5000)
				if consecutiveIneffective > 0 {
					cooldown = int64(5000) * int64(1<<uint(consecutiveIneffective))
					if cooldown > 120000 {
						cooldown = 120000
					}
				}

				if now-lastGCTime > cooldown {
					beforeGC := alloc
					runtime.GC()
					debug.FreeOSMemory()
					lastGCTime = now

					runtime.ReadMemStats(&ms)
					afterGC := int64(ms.Alloc)
					freed := beforeGC - afterGC

					if freed < beforeGC/20 {
						consecutiveIneffective++
						if now-lastLogTime > 60000 {
							lastLogTime = now
							log.Printf("内存占用: %d MB / 限制 %d MB（%.0f%%）- Pebble缓存占用为主，GC无法释放，已降低GC频率",
								afterGC/(1024*1024), l.MemoryBytes/(1024*1024),
								float64(afterGC)/float64(l.MemoryBytes)*100)
						}
					} else {
						consecutiveIneffective = 0
						log.Printf("内存回收: %d MB -> %d MB（释放 %d MB）",
							beforeGC/(1024*1024), afterGC/(1024*1024), freed/(1024*1024))
					}
				}
			} else if alloc > warningThreshold {
				now := time.Now().UnixMilli()
				if now-lastGCTime > 15000 {
					lastGCTime = now
					runtime.GC()
					consecutiveIneffective = 0
				}
			} else {
				consecutiveIneffective = 0
			}
		}
	}
}

func (l *Limits) ServerConcurrency(cpus int) int {
	conc := cpus * 4096

	if l.CPUCount > 0 {
		conc = l.CPUCount * 4096
	}

	if l.MemoryBytes > 0 {
		memConc := int(l.MemoryBytes / (16 * 1024))
		if memConc < conc {
			conc = memConc
		}
	}

	if conc < 256 {
		conc = 256
	}
	if conc > 262144 {
		conc = 262144
	}

	return conc
}

func (l *Limits) Summary() string {
	if !l.enabled {
		return "未启用"
	}
	parts := make([]string, 0, 2)
	if l.CPUCount > 0 {
		parts = append(parts, fmt.Sprintf("CPU %d核", l.CPUCount))
	}
	if l.MemoryBytes > 0 {
		parts = append(parts, fmt.Sprintf("内存 %dMB", l.MemoryBytes/(1024*1024)))
	}
	return strings.Join(parts, " | ")
}