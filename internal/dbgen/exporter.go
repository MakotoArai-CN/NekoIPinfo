package dbgen

import (
	"database/sql"
	"encoding/binary"
	"encoding/csv"
	"fmt"
	"net"
	"os"
	"strconv"

	_ "github.com/mattn/go-sqlite3"
)

func ip128ToIP(hi, lo uint64) net.IP {
	ip := make(net.IP, 16)
	binary.BigEndian.PutUint64(ip[:8], hi)
	binary.BigEndian.PutUint64(ip[8:16], lo)
	return ip
}

func formatIP(ip net.IP) string {
	ip16 := ip.To16()
	if ip16 == nil {
		return ip.String()
	}
	isV4 := true
	for i := 0; i < 10; i++ {
		if ip16[i] != 0 {
			isV4 = false
			break
		}
	}
	if isV4 && ip16[10] == 0xff && ip16[11] == 0xff {
		return net.IP(ip16[12:16]).To4().String()
	}
	return ip16.String()
}

func isIPv4Mapped(key []byte) bool {
	if len(key) < 16 {
		return false
	}
	for i := 0; i < 10; i++ {
		if key[i] != 0 {
			return false
		}
	}
	return key[10] == 0xff && key[11] == 0xff
}

func ipv4ToUint32(key []byte) uint32 {
	return uint32(key[12])<<24 | uint32(key[13])<<16 | uint32(key[14])<<8 | uint32(key[15])
}

func countRecords(dbPath string) (int64, error) {
	pdb, err := OpenPebbleReadOnly(dbPath)
	if err != nil {
		return 0, err
	}
	defer pdb.Close()

	iter, err := pdb.NewIter(nil)
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	var total int64
	for valid := iter.First(); valid; valid = iter.Next() {
		total++
	}
	return total, nil
}

func DumpToCSV(dbPath, outputPath string) (int64, error) {
	total, err := countRecords(dbPath)
	if err != nil {
		return 0, fmt.Errorf("统计记录数失败: %v", err)
	}

	pdb, err := OpenPebbleReadOnly(dbPath)
	if err != nil {
		return 0, fmt.Errorf("打开数据库失败: %v", err)
	}
	defer pdb.Close()

	outFile, err := os.Create(outputPath)
	if err != nil {
		return 0, fmt.Errorf("创建 CSV 文件失败: %v", err)
	}
	defer outFile.Close()

	writer := csv.NewWriter(outFile)
	defer writer.Flush()

	err = writer.Write([]string{"start_ip", "end_ip", "country", "province", "city", "isp", "latitude", "longitude"})
	if err != nil {
		return 0, fmt.Errorf("写入 CSV 头失败: %v", err)
	}

	iter, err := pdb.NewIter(nil)
	if err != nil {
		return 0, fmt.Errorf("创建迭代器失败: %v", err)
	}
	defer iter.Close()

	bar := NewNekoProgress("导出 CSV", total)
	bar.Start()

	var count int64
	for valid := iter.First(); valid; valid = iter.Next() {
		keyBytes := iter.Key()
		valBytes, valErr := iter.ValueAndErr()
		if valErr != nil || len(keyBytes) != 16 || len(valBytes) < 16 {
			continue
		}

		startHi, startLo := binary.BigEndian.Uint64(keyBytes[:8]), binary.BigEndian.Uint64(keyBytes[8:16])
		endHi, endLo := binary.BigEndian.Uint64(valBytes[:8]), binary.BigEndian.Uint64(valBytes[8:16])
		startIP := ip128ToIP(startHi, startLo)
		endIP := ip128ToIP(endHi, endLo)

		info, decErr := DecodePayload(valBytes[16:])
		if decErr != nil {
			continue
		}

		err = writer.Write([]string{
			formatIP(startIP),
			formatIP(endIP),
			info.Country,
			info.Province,
			info.City,
			info.ISP,
			info.Latitude,
			info.Longitude,
		})
		if err != nil {
			return count, fmt.Errorf("写入 CSV 行失败: %v", err)
		}

		count++
		if count%10000 == 0 {
			bar.SetCurrent(count)
			writer.Flush()
		}
	}

	bar.SetCurrent(count)
	bar.Finish()

	return count, nil
}

func DumpToSQLite(dbPath, outputPath string) (int64, error) {
	os.Remove(outputPath)

	total, err := countRecords(dbPath)
	if err != nil {
		return 0, fmt.Errorf("统计记录数失败: %v", err)
	}

	sqlDB, err := sql.Open("sqlite3", outputPath+"?_journal_mode=OFF&_synchronous=OFF&cache=shared")
	if err != nil {
		return 0, fmt.Errorf("创建 SQLite 数据库失败: %v", err)
	}
	defer sqlDB.Close()

	sqlDB.Exec("PRAGMA page_size=16384")

	_, err = sqlDB.Exec(`
		CREATE TABLE IF NOT EXISTS ip_info (
			network_start INTEGER NOT NULL,
			network_end INTEGER NOT NULL,
			country TEXT NOT NULL DEFAULT '',
			province TEXT NOT NULL DEFAULT '',
			city TEXT NOT NULL DEFAULT '',
			isp TEXT NOT NULL DEFAULT '',
			latitude REAL NOT NULL DEFAULT 0,
			longitude REAL NOT NULL DEFAULT 0
		)
	`)
	if err != nil {
		return 0, fmt.Errorf("创建 IPv4 表失败: %v", err)
	}

	_, err = sqlDB.Exec(`
		CREATE TABLE IF NOT EXISTS ip_info_v6 (
			network_start_hi INTEGER NOT NULL,
			network_start_lo INTEGER NOT NULL,
			network_end_hi INTEGER NOT NULL,
			network_end_lo INTEGER NOT NULL,
			country TEXT NOT NULL DEFAULT '',
			province TEXT NOT NULL DEFAULT '',
			city TEXT NOT NULL DEFAULT '',
			isp TEXT NOT NULL DEFAULT '',
			latitude REAL NOT NULL DEFAULT 0,
			longitude REAL NOT NULL DEFAULT 0
		)
	`)
	if err != nil {
		return 0, fmt.Errorf("创建 IPv6 表失败: %v", err)
	}

	pdb, err := OpenPebbleReadOnly(dbPath)
	if err != nil {
		return 0, fmt.Errorf("打开数据库失败: %v", err)
	}
	defer pdb.Close()

	iter, err := pdb.NewIter(nil)
	if err != nil {
		return 0, fmt.Errorf("创建迭代器失败: %v", err)
	}
	defer iter.Close()

	tx, err := sqlDB.Begin()
	if err != nil {
		return 0, fmt.Errorf("开始事务失败: %v", err)
	}

	stmtV4, err := tx.Prepare(`INSERT INTO ip_info (network_start, network_end, country, province, city, isp, latitude, longitude) VALUES (?,?,?,?,?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		return 0, fmt.Errorf("准备 IPv4 语句失败: %v", err)
	}

	stmtV6, err := tx.Prepare(`INSERT INTO ip_info_v6 (network_start_hi, network_start_lo, network_end_hi, network_end_lo, country, province, city, isp, latitude, longitude) VALUES (?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		return 0, fmt.Errorf("准备 IPv6 语句失败: %v", err)
	}

	bar := NewNekoProgress("导出 SQLite", total)
	bar.Start()

	var count int64
	batchLimit := int64(50000)

	for valid := iter.First(); valid; valid = iter.Next() {
		keyBytes := iter.Key()
		valBytes, valErr := iter.ValueAndErr()
		if valErr != nil || len(keyBytes) != 16 || len(valBytes) < 16 {
			continue
		}

		info, decErr := DecodePayload(valBytes[16:])
		if decErr != nil {
			continue
		}

		lat := parseFloat(info.Latitude)
		lon := parseFloat(info.Longitude)

		if isIPv4Mapped(keyBytes) {
			startU32 := ipv4ToUint32(keyBytes)
			endU32 := ipv4ToUint32(valBytes)

			_, err = stmtV4.Exec(
				int64(startU32), int64(endU32),
				info.Country, info.Province, info.City, info.ISP,
				lat, lon,
			)
		} else {
			startHi := int64(binary.BigEndian.Uint64(keyBytes[:8]))
			startLo := int64(binary.BigEndian.Uint64(keyBytes[8:16]))
			endHi := int64(binary.BigEndian.Uint64(valBytes[:8]))
			endLo := int64(binary.BigEndian.Uint64(valBytes[8:16]))

			_, err = stmtV6.Exec(
				startHi, startLo, endHi, endLo,
				info.Country, info.Province, info.City, info.ISP,
				lat, lon,
			)
		}

		if err != nil {
			tx.Rollback()
			return count, fmt.Errorf("插入数据失败: %v", err)
		}

		count++
		if count%batchLimit == 0 {
			bar.SetCurrent(count)

			stmtV4.Close()
			stmtV6.Close()
			if err := tx.Commit(); err != nil {
				return count, fmt.Errorf("提交事务失败: %v", err)
			}

			tx, err = sqlDB.Begin()
			if err != nil {
				return count, fmt.Errorf("开始事务失败: %v", err)
			}

			stmtV4, err = tx.Prepare(`INSERT INTO ip_info (network_start, network_end, country, province, city, isp, latitude, longitude) VALUES (?,?,?,?,?,?,?,?)`)
			if err != nil {
				tx.Rollback()
				return count, fmt.Errorf("准备 IPv4 语句失败: %v", err)
			}

			stmtV6, err = tx.Prepare(`INSERT INTO ip_info_v6 (network_start_hi, network_start_lo, network_end_hi, network_end_lo, country, province, city, isp, latitude, longitude) VALUES (?,?,?,?,?,?,?,?,?,?)`)
			if err != nil {
				tx.Rollback()
				return count, fmt.Errorf("准备 IPv6 语句失败: %v", err)
			}
		}
	}

	stmtV4.Close()
	stmtV6.Close()
	if err := tx.Commit(); err != nil {
		return count, fmt.Errorf("提交最终事务失败: %v", err)
	}

	bar.SetCurrent(count)
	bar.Finish()

	Neko("正在创建索引...", ColorMagenta)
	sqlDB.Exec("CREATE INDEX IF NOT EXISTS idx_v4_start ON ip_info(network_start)")
	sqlDB.Exec("CREATE INDEX IF NOT EXISTS idx_v6_start ON ip_info_v6(network_start_hi, network_start_lo)")

	return count, nil
}

func parseFloat(s string) float64 {
	if s == "" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

func DumpStats(dbPath string) error {
	pdb, err := OpenPebbleReadOnly(dbPath)
	if err != nil {
		return fmt.Errorf("打开数据库失败: %v", err)
	}
	defer pdb.Close()

	iter, err := pdb.NewIter(nil)
	if err != nil {
		return fmt.Errorf("创建迭代器失败: %v", err)
	}
	defer iter.Close()

	var totalRecords int64
	var totalPayloadBytes int64
	var ipv4Count int64
	var ipv6Count int64

	for valid := iter.First(); valid; valid = iter.Next() {
		keyBytes := iter.Key()
		valBytes, valErr := iter.ValueAndErr()
		if valErr != nil || len(keyBytes) != 16 || len(valBytes) < 16 {
			continue
		}

		totalRecords++
		totalPayloadBytes += int64(len(valBytes) - 16)

		if isIPv4Mapped(keyBytes) {
			ipv4Count++
		} else {
			ipv6Count++
		}
	}

	NekoHeader("数据库统计信息")
	Neko(fmt.Sprintf(" 总记录数: %d", totalRecords), ColorPink)
	Neko(fmt.Sprintf(" IPv4 记录: %d", ipv4Count), ColorPink)
	Neko(fmt.Sprintf(" IPv6 记录: %d", ipv6Count), ColorPink)
	Neko(fmt.Sprintf(" 数据负载量: %.2f MB", float64(totalPayloadBytes)/1024/1024), ColorPink)
	PrintDBSize(dbPath)
	NekoFooter()

	return nil
}

func DumpSample(dbPath string, limit int) error {
	pdb, err := OpenPebbleReadOnly(dbPath)
	if err != nil {
		return fmt.Errorf("打开数据库失败: %v", err)
	}
	defer pdb.Close()

	iter, err := pdb.NewIter(nil)
	if err != nil {
		return fmt.Errorf("创建迭代器失败: %v", err)
	}
	defer iter.Close()

	NekoHeader("数据库内容预览")

	count := 0
	for valid := iter.First(); valid && count < limit; valid = iter.Next() {
		keyBytes := iter.Key()
		valBytes, valErr := iter.ValueAndErr()
		if valErr != nil || len(keyBytes) != 16 || len(valBytes) < 16 {
			continue
		}

		startHi, startLo := binary.BigEndian.Uint64(keyBytes[:8]), binary.BigEndian.Uint64(keyBytes[8:16])
		endHi, endLo := binary.BigEndian.Uint64(valBytes[:8]), binary.BigEndian.Uint64(valBytes[8:16])
		startIP := ip128ToIP(startHi, startLo)
		endIP := ip128ToIP(endHi, endLo)

		Neko(fmt.Sprintf(" [%d] %s ~ %s", count+1, formatIP(startIP), formatIP(endIP)), ColorMagenta)
		fmt.Printf("  %s\n", PayloadToJSONString(valBytes[16:]))
		count++
	}

	Neko(fmt.Sprintf("\n 共显示 %d 条记录", count), ColorPink)
	NekoFooter()

	return nil
}