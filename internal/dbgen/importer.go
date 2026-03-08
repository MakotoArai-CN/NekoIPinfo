package dbgen

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"sort"
	"strings"

	json "github.com/goccy/go-json"

	"github.com/cockroachdb/pebble/v2"
	maxminddb "github.com/oschwald/maxminddb-golang"
)

func writeOrUpdate(pdb *pebble.DB, clDB *pebble.DB, batch *pebble.Batch, startIP, endIP net.IP, payload []byte, isUpdate bool, overwriteFlag bool, fieldsToUpdate []string) (written bool, err error) {
	start16 := startIP.To16()
	end16 := endIP.To16()
	if start16 == nil || end16 == nil {
		return false, fmt.Errorf("invalid IP")
	}

	key := MakeKey(startIP)

	if !isUpdate {
		val := MakeValue(endIP, payload)
		if val == nil {
			return false, fmt.Errorf("invalid end IP")
		}
		return true, batch.Set(key[:], val, nil)
	}

	existing, closer, getErr := pdb.Get(key[:])
	if getErr != nil {
		val := MakeValue(endIP, payload)
		if val == nil {
			return false, fmt.Errorf("invalid end IP")
		}
		RecordChange(clDB, key, "insert", nil, PayloadToJSON(payload))
		return true, batch.Set(key[:], val, nil)
	}
	defer closer.Close()

	if len(existing) < 16 {
		val := MakeValue(endIP, payload)
		if val == nil {
			return false, fmt.Errorf("invalid end IP")
		}
		RecordChange(clDB, key, "insert", nil, PayloadToJSON(payload))
		return true, batch.Set(key[:], val, nil)
	}

	existingPayload := existing[16:]

	if overwriteFlag {
		if bytes.Equal(existingPayload, payload) {
			return false, nil
		}
		val := MakeValue(endIP, payload)
		if val == nil {
			return false, fmt.Errorf("invalid end IP")
		}
		RecordChange(clDB, key, "overwrite", PayloadToJSON(existingPayload), PayloadToJSON(payload))
		return true, batch.Set(key[:], val, nil)
	}

	if len(fieldsToUpdate) > 0 {
		merged, changed := MergeFields(existingPayload, payload, fieldsToUpdate)
		if !changed {
			return false, nil
		}
		val := MakeValue(endIP, merged)
		if val == nil {
			return false, fmt.Errorf("invalid end IP")
		}
		RecordChange(clDB, key, "merge", PayloadToJSON(existingPayload), PayloadToJSON(merged))
		return true, batch.Set(key[:], val, nil)
	}

	if bytes.Equal(existingPayload, payload) {
		return false, nil
	}

	val := MakeValue(endIP, payload)
	if val == nil {
		return false, fmt.Errorf("invalid end IP")
	}
	RecordChange(clDB, key, "update", PayloadToJSON(existingPayload), PayloadToJSON(payload))
	return true, batch.Set(key[:], val, nil)
}

func ImportMMDB(mmdbPath string, pdb *pebble.DB, clDB *pebble.DB, asnDB *maxminddb.Reader, isUpdate bool, overwriteFlag bool, fieldsToUpdate []string) (int64, int64, error) {
	reader, err := maxminddb.Open(mmdbPath)
	if err != nil {
		return 0, 0, fmt.Errorf("无法打开 MMDB 文件: %v", err)
	}
	defer reader.Close()

	metadata := reader.Metadata
	totalEst := int64(metadata.NodeCount)
	if totalEst <= 0 {
		totalEst = 10000000
	}

	batch := pdb.NewBatch()
	defer batch.Close()

	var count int64
	var skipped int64

	bar := NewNekoProgress("导入 MMDB", totalEst)
	bar.Start()

	networks := reader.Networks(maxminddb.SkipAliasedNetworks)
	for networks.Next() {
		var record MmdbRecord
		network, err := networks.Network(&record)
		if err != nil {
			skipped++
			continue
		}

		startIP := network.IP.To16()
		if startIP == nil {
			skipped++
			continue
		}

		endIP := LastIPInNetwork(network)
		if endIP == nil {
			skipped++
			continue
		}

		country := GetName(record.Country.Names)
		province := ""
		if len(record.Subdivisions) > 0 {
			province = GetName(record.Subdivisions[0].Names)
		}
		city := GetName(record.City.Names)

		isp := record.Traits.ISP
		if isp == "" {
			isp = record.Traits.Organization
		}
		if isp == "" && asnDB != nil {
			isp = LookupASN(asnDB, network.IP)
		}

		latitude := ""
		longitude := ""
		if record.Location.Latitude != 0 || record.Location.Longitude != 0 {
			latitude = FloatToString(record.Location.Latitude)
			longitude = FloatToString(record.Location.Longitude)
		}

		if country == "" && province == "" && city == "" &&
			math.Abs(record.Location.Latitude) < 0.0001 &&
			math.Abs(record.Location.Longitude) < 0.0001 {
			skipped++
			continue
		}

		infoMap := IPInfoFields{
			Country:   country,
			Province:  province,
			City:      city,
			ISP:       isp,
			Latitude:  latitude,
			Longitude: longitude,
		}

		payload := EncodeFields(&infoMap)

		written, err := writeOrUpdate(pdb, clDB, batch, startIP, endIP, payload, isUpdate, overwriteFlag, fieldsToUpdate)
		if err != nil {
			skipped++
			continue
		}

		if written {
			count++
		} else {
			skipped++
		}

		processed := count + skipped
		if processed%10000 == 0 {
			bar.SetCurrent(processed)
		}

		if count > 0 && count%int64(BatchSize) == 0 {
			if err := commitBatchSilent(batch); err != nil {
				bar.Finish()
				return count, skipped, err
			}
		}
	}

	if err := networks.Err(); err != nil {
		bar.Finish()
		return count, skipped, fmt.Errorf("遍历 MMDB 出错: %v", err)
	}

	if err := batch.Commit(pebble.NoSync); err != nil {
		bar.Finish()
		return count, skipped, fmt.Errorf("提交最终批次失败: %v", err)
	}

	bar.SetCurrent(count + skipped)
	bar.Finish()

	return count, skipped, nil
}

func ImportASNOnly(asnPath string, pdb *pebble.DB, clDB *pebble.DB, overwriteFlag bool) (int64, int64, error) {
	asnReader, err := maxminddb.Open(asnPath)
	if err != nil {
		return 0, 0, fmt.Errorf("无法打开 ASN MMDB: %v", err)
	}
	defer asnReader.Close()

	totalEst := int64(asnReader.Metadata.NodeCount)
	if totalEst <= 0 {
		totalEst = 1000000
	}

	batch := pdb.NewBatch()
	defer batch.Close()

	var count int64
	var skipped int64

	bar := NewNekoProgress("导入 ASN", totalEst)
	bar.Start()

	networks := asnReader.Networks(maxminddb.SkipAliasedNetworks)
	for networks.Next() {
		var record AsnRecord
		network, err := networks.Network(&record)
		if err != nil {
			skipped++
			continue
		}

		startIP := network.IP.To16()
		if startIP == nil {
			skipped++
			continue
		}

		endIP := LastIPInNetwork(network)
		if endIP == nil {
			skipped++
			continue
		}

		isp := record.ISP
		if isp == "" {
			isp = record.Organization
		}
		if isp == "" {
			isp = record.AutonomousSystemOrganization
		}
		if isp == "" {
			skipped++
			continue
		}

		infoMap := IPInfoFields{ISP: isp}
		payload := EncodeFields(&infoMap)

		fieldsToUpdate := []string{"isp"}
		written, err := writeOrUpdate(pdb, clDB, batch, startIP, endIP, payload, true, overwriteFlag, fieldsToUpdate)
		if err != nil {
			skipped++
			continue
		}

		if written {
			count++
		} else {
			skipped++
		}

		processed := count + skipped
		if processed%10000 == 0 {
			bar.SetCurrent(processed)
		}

		if count > 0 && count%int64(BatchSize) == 0 {
			if err := commitBatchSilent(batch); err != nil {
				bar.Finish()
				return count, skipped, err
			}
		}
	}

	if err := networks.Err(); err != nil {
		bar.Finish()
		return count, skipped, fmt.Errorf("遍历 ASN MMDB 出错: %v", err)
	}

	if err := batch.Commit(pebble.NoSync); err != nil {
		bar.Finish()
		return count, skipped, fmt.Errorf("提交最终批次失败: %v", err)
	}

	bar.SetCurrent(count + skipped)
	bar.Finish()

	return count, skipped, nil
}

func ImportCSV(csvPath string, pdb *pebble.DB, clDB *pebble.DB, asnDB *maxminddb.Reader, isUpdate bool, overwriteFlag bool, fieldsToUpdate []string) (int64, int64, error) {
	csvFile, err := os.Open(csvPath)
	if err != nil {
		return 0, 0, fmt.Errorf("无法打开 CSV 文件: %v", err)
	}
	defer csvFile.Close()

	fi, err := csvFile.Stat()
	if err != nil {
		return 0, 0, fmt.Errorf("获取文件信息失败: %v", err)
	}
	totalEst := fi.Size() / 80
	if totalEst <= 0 {
		totalEst = 1000000
	}

	batch := pdb.NewBatch()
	defer batch.Close()

	csvReader := csv.NewReader(csvFile)
	csvReader.FieldsPerRecord = -1

	var count int64
	var skipped int64

	bar := NewNekoProgress("导入 CSV", totalEst)
	bar.Start()

	for {
		csvRecord, err := csvReader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			skipped++
			continue
		}

		if len(csvRecord) < 2 {
			skipped++
			continue
		}

		startIP := net.ParseIP(csvRecord[0])
		endIP := net.ParseIP(csvRecord[1])
		if startIP == nil || endIP == nil {
			skipped++
			continue
		}

		country := ""
		province := ""
		city := ""
		latitude := ""
		longitude := ""

		if len(csvRecord) >= 4 {
			country = csvRecord[3]
		}
		if len(csvRecord) >= 5 {
			province = csvRecord[4]
		}
		if len(csvRecord) >= 6 {
			city = csvRecord[5]
		}
		if len(csvRecord) >= 7 {
			latitude = csvRecord[6]
		}
		if len(csvRecord) >= 8 {
			longitude = csvRecord[7]
		}

		isp := ""
		if asnDB != nil {
			isp = LookupASN(asnDB, startIP)
		}

		infoMap := IPInfoFields{
			Country:   country,
			Province:  province,
			City:      city,
			ISP:       isp,
			Latitude:  latitude,
			Longitude: longitude,
		}

		payload := EncodeFields(&infoMap)

		written, err := writeOrUpdate(pdb, clDB, batch, startIP, endIP, payload, isUpdate, overwriteFlag, fieldsToUpdate)
		if err != nil {
			skipped++
			continue
		}

		if written {
			count++
		} else {
			skipped++
		}

		processed := count + skipped
		if processed%10000 == 0 {
			bar.SetCurrent(processed)
		}

		if count > 0 && count%int64(BatchSize) == 0 {
			if err := commitBatchSilent(batch); err != nil {
				bar.Finish()
				return count, skipped, err
			}
		}
	}

	if err := batch.Commit(pebble.NoSync); err != nil {
		bar.Finish()
		return count, skipped, fmt.Errorf("提交最终批次失败: %v", err)
	}

	bar.SetCurrent(count + skipped)
	bar.Finish()

	return count, skipped, nil
}

func ImportSQLite(sqlitePath string, pdb *pebble.DB, clDB *pebble.DB, asnDB *maxminddb.Reader, isUpdate bool, overwriteFlag bool, fieldsToUpdate []string) (int64, int64, error) {
	sqlDB, err := sql.Open("sqlite3", sqlitePath+"?mode=ro")
	if err != nil {
		return 0, 0, fmt.Errorf("无法打开 SQLite 数据库: %v", err)
	}
	defer sqlDB.Close()

	v4Schema := detectV4Schema(sqlDB)
	v6Exists := tableExists(sqlDB, "ip_info_v6")
	var v6Schema string
	if v6Exists {
		v6Schema = detectV6Schema(sqlDB)
	}

	var totalEst int64
	var tmp int64
	if sqlDB.QueryRow("SELECT COUNT(*) FROM ip_info").Scan(&tmp) == nil {
		totalEst += tmp
	}
	if v6Exists {
		if sqlDB.QueryRow("SELECT COUNT(*) FROM ip_info_v6").Scan(&tmp) == nil {
			totalEst += tmp
		}
	}
	if totalEst <= 0 {
		totalEst = 1000000
	}

	batch := pdb.NewBatch()
	defer batch.Close()

	var count int64
	var skipped int64

	bar := NewNekoProgress("导入 SQLite", totalEst)
	bar.Start()

	switch v4Schema {
	case "columns":
		c, s, err := importSQLiteV4Columns(sqlDB, pdb, clDB, asnDB, batch, isUpdate, overwriteFlag, fieldsToUpdate, bar, count, skipped)
		if err != nil {
			bar.Finish()
			return c, s, err
		}
		count = c
		skipped = s
	default:
		c, s, err := importSQLiteV4JSON(sqlDB, pdb, clDB, asnDB, batch, isUpdate, overwriteFlag, fieldsToUpdate, bar, count, skipped)
		if err != nil {
			bar.Finish()
			return c, s, err
		}
		count = c
		skipped = s
	}

	if v6Exists {
		switch v6Schema {
		case "columns":
			c, s, err := importSQLiteV6Columns(sqlDB, pdb, clDB, asnDB, batch, isUpdate, overwriteFlag, fieldsToUpdate, bar, count, skipped)
			if err != nil {
				bar.Finish()
				return c, s, err
			}
			count = c
			skipped = s
		default:
			c, s, err := importSQLiteV6JSON(sqlDB, pdb, clDB, asnDB, batch, isUpdate, overwriteFlag, fieldsToUpdate, bar, count, skipped)
			if err != nil {
				bar.Finish()
				return c, s, err
			}
			count = c
			skipped = s
		}
	}

	if err := batch.Commit(pebble.NoSync); err != nil {
		bar.Finish()
		return count, skipped, fmt.Errorf("提交最终批次失败: %v", err)
	}

	bar.SetCurrent(count + skipped)
	bar.Finish()

	return count, skipped, nil
}

func tableExists(sqlDB *sql.DB, name string) bool {
	var cnt int
	err := sqlDB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&cnt)
	return err == nil && cnt > 0
}

func detectV4Schema(sqlDB *sql.DB) string {
	var cnt int
	err := sqlDB.QueryRow("SELECT COUNT(*) FROM pragma_table_info('ip_info') WHERE name='country'").Scan(&cnt)
	if err == nil && cnt > 0 {
		return "columns"
	}
	return "json"
}

func detectV6Schema(sqlDB *sql.DB) string {
	var cnt int
	err := sqlDB.QueryRow("SELECT COUNT(*) FROM pragma_table_info('ip_info_v6') WHERE name='country'").Scan(&cnt)
	if err == nil && cnt > 0 {
		return "columns"
	}
	return "json"
}

func buildV4IP(u32 uint32) net.IP {
	ip := make(net.IP, 16)
	copy(ip[:12], []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff})
	ip[12] = byte(u32 >> 24)
	ip[13] = byte(u32 >> 16)
	ip[14] = byte(u32 >> 8)
	ip[15] = byte(u32)
	return ip
}

func floatToStr(f float64) string {
	if f == 0 {
		return ""
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.6f", f), "0"), ".")
}

func importSQLiteV4Columns(sqlDB *sql.DB, pdb *pebble.DB, clDB *pebble.DB, asnDB *maxminddb.Reader, batch *pebble.Batch, isUpdate bool, overwriteFlag bool, fieldsToUpdate []string, bar *NekoProgress, count, skipped int64) (int64, int64, error) {
	rows, err := sqlDB.Query(`SELECT network_start, network_end, country, province, city, isp, latitude, longitude FROM ip_info ORDER BY network_start ASC`)
	if err != nil {
		return count, skipped, fmt.Errorf("查询 IPv4 列存表失败: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var startInt, endInt int64
		var country, province, city, isp string
		var lat, lon float64

		if err := rows.Scan(&startInt, &endInt, &country, &province, &city, &isp, &lat, &lon); err != nil {
			skipped++
			continue
		}

		startIP := buildV4IP(uint32(startInt))
		endIP := buildV4IP(uint32(endInt))

		if isp == "" && asnDB != nil {
			isp = LookupASN(asnDB, startIP)
		}

		infoMap := IPInfoFields{
			Country:   country,
			Province:  province,
			City:      city,
			ISP:       isp,
			Latitude:  floatToStr(lat),
			Longitude: floatToStr(lon),
		}

		payload := EncodeFields(&infoMap)

		written, err := writeOrUpdate(pdb, clDB, batch, startIP, endIP, payload, isUpdate, overwriteFlag, fieldsToUpdate)
		if err != nil {
			skipped++
			continue
		}

		if written {
			count++
		} else {
			skipped++
		}

		processed := count + skipped
		if processed%10000 == 0 {
			bar.SetCurrent(processed)
		}

		if count > 0 && count%int64(BatchSize) == 0 {
			if err := commitBatchSilent(batch); err != nil {
				return count, skipped, err
			}
		}
	}

	return count, skipped, nil
}

func importSQLiteV4JSON(sqlDB *sql.DB, pdb *pebble.DB, clDB *pebble.DB, asnDB *maxminddb.Reader, batch *pebble.Batch, isUpdate bool, overwriteFlag bool, fieldsToUpdate []string, bar *NekoProgress, count, skipped int64) (int64, int64, error) {
	rows, err := sqlDB.Query(`SELECT network_start, network_end, ip_info_json FROM ip_info ORDER BY network_start ASC`)
	if err != nil {
		return count, skipped, fmt.Errorf("查询 IPv4 JSON 表失败: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var startInt, endInt int64
		var infoJSON string

		if err := rows.Scan(&startInt, &endInt, &infoJSON); err != nil {
			skipped++
			continue
		}

		startIP := buildV4IP(uint32(startInt))
		endIP := buildV4IP(uint32(endInt))

		payload := normalizeAndEncode([]byte(infoJSON), asnDB, startIP)

		written, err := writeOrUpdate(pdb, clDB, batch, startIP, endIP, payload, isUpdate, overwriteFlag, fieldsToUpdate)
		if err != nil {
			skipped++
			continue
		}

		if written {
			count++
		} else {
			skipped++
		}

		processed := count + skipped
		if processed%10000 == 0 {
			bar.SetCurrent(processed)
		}

		if count > 0 && count%int64(BatchSize) == 0 {
			if err := commitBatchSilent(batch); err != nil {
				return count, skipped, err
			}
		}
	}

	return count, skipped, nil
}

func importSQLiteV6Columns(sqlDB *sql.DB, pdb *pebble.DB, clDB *pebble.DB, asnDB *maxminddb.Reader, batch *pebble.Batch, isUpdate bool, overwriteFlag bool, fieldsToUpdate []string, bar *NekoProgress, count, skipped int64) (int64, int64, error) {
	rows, err := sqlDB.Query(`SELECT network_start_hi, network_start_lo, network_end_hi, network_end_lo, country, province, city, isp, latitude, longitude FROM ip_info_v6 ORDER BY network_start_hi ASC, network_start_lo ASC`)
	if err != nil {
		return count, skipped, fmt.Errorf("查询 IPv6 列存表失败: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var startHi, startLo, endHi, endLo int64
		var country, province, city, isp string
		var lat, lon float64

		if err := rows.Scan(&startHi, &startLo, &endHi, &endLo, &country, &province, &city, &isp, &lat, &lon); err != nil {
			skipped++
			continue
		}

		startIP := make(net.IP, 16)
		binary.BigEndian.PutUint64(startIP[:8], uint64(startHi))
		binary.BigEndian.PutUint64(startIP[8:16], uint64(startLo))

		endIP := make(net.IP, 16)
		binary.BigEndian.PutUint64(endIP[:8], uint64(endHi))
		binary.BigEndian.PutUint64(endIP[8:16], uint64(endLo))

		if isp == "" && asnDB != nil {
			isp = LookupASN(asnDB, startIP)
		}

		infoMap := IPInfoFields{
			Country:   country,
			Province:  province,
			City:      city,
			ISP:       isp,
			Latitude:  floatToStr(lat),
			Longitude: floatToStr(lon),
		}

		payload := EncodeFields(&infoMap)

		written, err := writeOrUpdate(pdb, clDB, batch, startIP, endIP, payload, isUpdate, overwriteFlag, fieldsToUpdate)
		if err != nil {
			skipped++
			continue
		}

		if written {
			count++
		} else {
			skipped++
		}

		processed := count + skipped
		if processed%10000 == 0 {
			bar.SetCurrent(processed)
		}

		if count > 0 && count%int64(BatchSize) == 0 {
			if err := commitBatchSilent(batch); err != nil {
				return count, skipped, err
			}
		}
	}

	return count, skipped, nil
}

func importSQLiteV6JSON(sqlDB *sql.DB, pdb *pebble.DB, clDB *pebble.DB, asnDB *maxminddb.Reader, batch *pebble.Batch, isUpdate bool, overwriteFlag bool, fieldsToUpdate []string, bar *NekoProgress, count, skipped int64) (int64, int64, error) {
	rows, err := sqlDB.Query(`SELECT network_start_hi, network_start_lo, network_end_hi, network_end_lo, ip_info_json FROM ip_info_v6 ORDER BY network_start_hi ASC, network_start_lo ASC`)
	if err != nil {
		return count, skipped, fmt.Errorf("查询 IPv6 JSON 表失败: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var startHi, startLo, endHi, endLo int64
		var infoJSON string

		if err := rows.Scan(&startHi, &startLo, &endHi, &endLo, &infoJSON); err != nil {
			skipped++
			continue
		}

		startIP := make(net.IP, 16)
		binary.BigEndian.PutUint64(startIP[:8], uint64(startHi))
		binary.BigEndian.PutUint64(startIP[8:16], uint64(startLo))

		endIP := make(net.IP, 16)
		binary.BigEndian.PutUint64(endIP[:8], uint64(endHi))
		binary.BigEndian.PutUint64(endIP[8:16], uint64(endLo))

		payload := normalizeAndEncode([]byte(infoJSON), asnDB, startIP)

		written, err := writeOrUpdate(pdb, clDB, batch, startIP, endIP, payload, isUpdate, overwriteFlag, fieldsToUpdate)
		if err != nil {
			skipped++
			continue
		}

		if written {
			count++
		} else {
			skipped++
		}

		processed := count + skipped
		if processed%10000 == 0 {
			bar.SetCurrent(processed)
		}

		if count > 0 && count%int64(BatchSize) == 0 {
			if err := commitBatchSilent(batch); err != nil {
				return count, skipped, err
			}
		}
	}

	return count, skipped, nil
}

func normalizeAndEncode(raw []byte, asnDB *maxminddb.Reader, ip net.IP) []byte {
	var rawData map[string]string
	if err := json.Unmarshal(raw, &rawData); err != nil {
		return JSONToPayload(raw)
	}

	isp := rawData["isp"]
	if isp == "" && asnDB != nil {
		isp = LookupASN(asnDB, ip)
	}

	infoMap := IPInfoFields{
		Country:   rawData["country"],
		Province:  rawData["province"],
		City:      rawData["city"],
		ISP:       isp,
		Latitude:  rawData["latitude"],
		Longitude: rawData["longitude"],
	}
	return EncodeFields(&infoMap)
}

type sortedEntry struct {
	key     [16]byte
	startIP net.IP
	endIP   net.IP
	payload []byte
}

type sortedEntries []sortedEntry

func (s sortedEntries) Len() int      { return len(s) }
func (s sortedEntries) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
func (s sortedEntries) Less(i, j int) bool {
	return bytes.Compare(s[i].key[:], s[j].key[:]) < 0
}

func ImportIncrementalMMDB(mmdbPath string, pdb *pebble.DB, clDB *pebble.DB, asnDB *maxminddb.Reader, overwriteFlag bool, fieldsToUpdate []string) (int64, int64, error) {
	reader, err := maxminddb.Open(mmdbPath)
	if err != nil {
		return 0, 0, fmt.Errorf("无法打开 MMDB 文件: %v", err)
	}
	defer reader.Close()

	totalEst := int64(reader.Metadata.NodeCount)
	if totalEst <= 0 {
		totalEst = 10000000
	}

	NekoSection("阶段 1/3: 读取 MMDB 数据")
	bar1 := NewNekoProgress("读取 MMDB", totalEst)
	bar1.Start()

	entries := make(sortedEntries, 0, 1<<20)
	var readCount int64

	networks := reader.Networks(maxminddb.SkipAliasedNetworks)
	for networks.Next() {
		var record MmdbRecord
		network, err := networks.Network(&record)
		if err != nil {
			continue
		}

		startIP := network.IP.To16()
		if startIP == nil {
			continue
		}

		endIP := LastIPInNetwork(network)
		if endIP == nil {
			continue
		}

		country := GetName(record.Country.Names)
		province := ""
		if len(record.Subdivisions) > 0 {
			province = GetName(record.Subdivisions[0].Names)
		}
		city := GetName(record.City.Names)

		isp := record.Traits.ISP
		if isp == "" {
			isp = record.Traits.Organization
		}
		if isp == "" && asnDB != nil {
			isp = LookupASN(asnDB, network.IP)
		}

		latitude := ""
		longitude := ""
		if record.Location.Latitude != 0 || record.Location.Longitude != 0 {
			latitude = FloatToString(record.Location.Latitude)
			longitude = FloatToString(record.Location.Longitude)
		}

		if country == "" && province == "" && city == "" &&
			math.Abs(record.Location.Latitude) < 0.0001 &&
			math.Abs(record.Location.Longitude) < 0.0001 {
			continue
		}

		infoMap := IPInfoFields{
			Country:   country,
			Province:  province,
			City:      city,
			ISP:       isp,
			Latitude:  latitude,
			Longitude: longitude,
		}

		payload := EncodeFields(&infoMap)

		key := MakeKey(startIP)
		startCopy := make(net.IP, 16)
		copy(startCopy, startIP)
		endCopy := make(net.IP, 16)
		copy(endCopy, endIP)

		entries = append(entries, sortedEntry{
			key:     key,
			startIP: startCopy,
			endIP:   endCopy,
			payload: payload,
		})

		readCount++
		if readCount%10000 == 0 {
			bar1.SetCurrent(readCount)
		}
	}

	if err := networks.Err(); err != nil {
		bar1.Finish()
		return 0, 0, fmt.Errorf("遍历 MMDB 出错: %v", err)
	}

	bar1.SetCurrent(readCount)
	bar1.Finish()

	Neko(fmt.Sprintf(" 读取完成，共 %d 条有效记录", len(entries)), ColorLavend)

	if !sort.IsSorted(entries) {
		Neko(" 正在排序...", ColorLavend)
		sort.Sort(entries)
	}

	NekoSection("阶段 2/3: 双指针归并比较")
	bar2 := NewNekoProgress("归并比较", int64(len(entries)))
	bar2.Start()

	iter, err := pdb.NewIter(nil)
	if err != nil {
		bar2.Finish()
		return 0, 0, fmt.Errorf("创建数据库迭代器失败: %v", err)
	}
	defer iter.Close()

	batch := pdb.NewBatch()
	defer batch.Close()

	var count int64
	var skipped int64

	dbValid := iter.First()
	newIdx := 0

	for newIdx < len(entries) {
		entry := &entries[newIdx]

		if !dbValid {
			val := MakeValue(entry.endIP, entry.payload)
			if val != nil {
				RecordChange(clDB, entry.key, "insert", nil, PayloadToJSON(entry.payload))
				batch.Set(entry.key[:], val, nil)
				count++
			}
			newIdx++
			if newIdx%10000 == 0 {
				bar2.SetCurrent(int64(newIdx))
			}
			if count > 0 && count%int64(BatchSize) == 0 {
				if err := commitBatchSilent(batch); err != nil {
					bar2.Finish()
					return count, skipped, err
				}
			}
			continue
		}

		dbKey := iter.Key()
		cmp := bytes.Compare(entry.key[:], dbKey)

		if cmp < 0 {
			val := MakeValue(entry.endIP, entry.payload)
			if val != nil {
				RecordChange(clDB, entry.key, "insert", nil, PayloadToJSON(entry.payload))
				batch.Set(entry.key[:], val, nil)
				count++
			}
			newIdx++
		} else if cmp > 0 {
			dbValid = iter.Next()
		} else {
			dbVal, valErr := iter.ValueAndErr()
			if valErr != nil || len(dbVal) < 16 {
				val := MakeValue(entry.endIP, entry.payload)
				if val != nil {
					RecordChange(clDB, entry.key, "insert", nil, PayloadToJSON(entry.payload))
					batch.Set(entry.key[:], val, nil)
					count++
				}
				newIdx++
				dbValid = iter.Next()
				if newIdx%10000 == 0 {
					bar2.SetCurrent(int64(newIdx))
				}
				continue
			}

			existingPayload := dbVal[16:]

			if overwriteFlag {
				if !bytes.Equal(existingPayload, entry.payload) {
					val := MakeValue(entry.endIP, entry.payload)
					if val != nil {
						RecordChange(clDB, entry.key, "overwrite", PayloadToJSON(existingPayload), PayloadToJSON(entry.payload))
						batch.Set(entry.key[:], val, nil)
						count++
					} else {
						skipped++
					}
				} else {
					skipped++
				}
			} else if len(fieldsToUpdate) > 0 {
				merged, changed := MergeFields(existingPayload, entry.payload, fieldsToUpdate)
				if changed {
					val := MakeValue(entry.endIP, merged)
					if val != nil {
						RecordChange(clDB, entry.key, "merge", PayloadToJSON(existingPayload), PayloadToJSON(merged))
						batch.Set(entry.key[:], val, nil)
						count++
					} else {
						skipped++
					}
				} else {
					skipped++
				}
			} else {
				if !bytes.Equal(existingPayload, entry.payload) {
					val := MakeValue(entry.endIP, entry.payload)
					if val != nil {
						RecordChange(clDB, entry.key, "update", PayloadToJSON(existingPayload), PayloadToJSON(entry.payload))
						batch.Set(entry.key[:], val, nil)
						count++
					} else {
						skipped++
					}
				} else {
					skipped++
				}
			}

			newIdx++
			dbValid = iter.Next()
		}

		if newIdx%10000 == 0 {
			bar2.SetCurrent(int64(newIdx))
		}

		if count > 0 && count%int64(BatchSize) == 0 {
			if err := commitBatchSilent(batch); err != nil {
				bar2.Finish()
				return count, skipped, err
			}
		}
	}

	bar2.SetCurrent(int64(len(entries)))
	bar2.Finish()

	NekoSection("阶段 3/3: 提交变更")

	if err := batch.Commit(pebble.NoSync); err != nil {
		return count, skipped, fmt.Errorf("提交最终批次失败: %v", err)
	}

	NekoSuccess(fmt.Sprintf("变更已提交，更新 %d 条，跳过 %d 条", count, skipped))

	return count, skipped, nil
}

func ImportIncrementalASN(asnPath string, pdb *pebble.DB, clDB *pebble.DB, overwriteFlag bool) (int64, int64, error) {
	asnReader, err := maxminddb.Open(asnPath)
	if err != nil {
		return 0, 0, fmt.Errorf("无法打开 ASN MMDB: %v", err)
	}
	defer asnReader.Close()

	totalEst := int64(asnReader.Metadata.NodeCount)
	if totalEst <= 0 {
		totalEst = 1000000
	}

	type asnEntry struct {
		key     [16]byte
		startIP net.IP
		endIP   net.IP
		isp     string
	}

	NekoSection("阶段 1/3: 读取 ASN 数据")
	bar1 := NewNekoProgress("读取 ASN", totalEst)
	bar1.Start()

	asnEntries := make([]asnEntry, 0, 1<<19)
	var readCount int64

	networks := asnReader.Networks(maxminddb.SkipAliasedNetworks)
	for networks.Next() {
		var record AsnRecord
		network, err := networks.Network(&record)
		if err != nil {
			continue
		}

		startIP := network.IP.To16()
		if startIP == nil {
			continue
		}

		endIP := LastIPInNetwork(network)
		if endIP == nil {
			continue
		}

		isp := record.ISP
		if isp == "" {
			isp = record.Organization
		}
		if isp == "" {
			isp = record.AutonomousSystemOrganization
		}
		if isp == "" {
			continue
		}

		key := MakeKey(startIP)
		startCopy := make(net.IP, 16)
		copy(startCopy, startIP)
		endCopy := make(net.IP, 16)
		copy(endCopy, endIP)

		asnEntries = append(asnEntries, asnEntry{
			key:     key,
			startIP: startCopy,
			endIP:   endCopy,
			isp:     isp,
		})

		readCount++
		if readCount%10000 == 0 {
			bar1.SetCurrent(readCount)
		}
	}

	if err := networks.Err(); err != nil {
		bar1.Finish()
		return 0, 0, fmt.Errorf("遍历 ASN MMDB 出错: %v", err)
	}

	bar1.SetCurrent(readCount)
	bar1.Finish()

	Neko(fmt.Sprintf(" 读取完成，共 %d 条有效记录", len(asnEntries)), ColorLavend)

	needSort := false
	for i := 1; i < len(asnEntries); i++ {
		if bytes.Compare(asnEntries[i-1].key[:], asnEntries[i].key[:]) > 0 {
			needSort = true
			break
		}
	}
	if needSort {
		Neko(" 正在排序...", ColorLavend)
		sort.Slice(asnEntries, func(i, j int) bool {
			return bytes.Compare(asnEntries[i].key[:], asnEntries[j].key[:]) < 0
		})
	}

	NekoSection("阶段 2/3: 双指针归并比较")
	bar2 := NewNekoProgress("归并比较", int64(len(asnEntries)))
	bar2.Start()

	iter, err := pdb.NewIter(nil)
	if err != nil {
		bar2.Finish()
		return 0, 0, fmt.Errorf("创建数据库迭代器失败: %v", err)
	}
	defer iter.Close()

	batch := pdb.NewBatch()
	defer batch.Close()

	var count int64
	var skipped int64

	fieldsToUpdate := []string{"isp"}

	dbValid := iter.First()
	newIdx := 0

	for newIdx < len(asnEntries) {
		entry := &asnEntries[newIdx]

		ispInfo := IPInfoFields{ISP: entry.isp}
		payload := EncodeFields(&ispInfo)

		if !dbValid {
			val := MakeValue(entry.endIP, payload)
			if val != nil {
				RecordChange(clDB, entry.key, "insert", nil, PayloadToJSON(payload))
				batch.Set(entry.key[:], val, nil)
				count++
			}
			newIdx++
			if newIdx%10000 == 0 {
				bar2.SetCurrent(int64(newIdx))
			}
			if count > 0 && count%int64(BatchSize) == 0 {
				if err := commitBatchSilent(batch); err != nil {
					bar2.Finish()
					return count, skipped, err
				}
			}
			continue
		}

		dbKey := iter.Key()
		cmp := bytes.Compare(entry.key[:], dbKey)

		if cmp < 0 {
			val := MakeValue(entry.endIP, payload)
			if val != nil {
				RecordChange(clDB, entry.key, "insert", nil, PayloadToJSON(payload))
				batch.Set(entry.key[:], val, nil)
				count++
			}
			newIdx++
		} else if cmp > 0 {
			dbValid = iter.Next()
		} else {
			dbVal, valErr := iter.ValueAndErr()
			if valErr != nil || len(dbVal) < 16 {
				val := MakeValue(entry.endIP, payload)
				if val != nil {
					batch.Set(entry.key[:], val, nil)
					count++
				}
				newIdx++
				dbValid = iter.Next()
				if newIdx%10000 == 0 {
					bar2.SetCurrent(int64(newIdx))
				}
				continue
			}

			existingPayload := dbVal[16:]

			if overwriteFlag {
				if !bytes.Equal(existingPayload, payload) {
					val := MakeValue(entry.endIP, payload)
					if val != nil {
						RecordChange(clDB, entry.key, "overwrite", PayloadToJSON(existingPayload), PayloadToJSON(payload))
						batch.Set(entry.key[:], val, nil)
						count++
					} else {
						skipped++
					}
				} else {
					skipped++
				}
			} else {
				merged, changed := MergeFields(existingPayload, payload, fieldsToUpdate)
				if changed {
					val := MakeValue(entry.endIP, merged)
					if val != nil {
						RecordChange(clDB, entry.key, "merge", PayloadToJSON(existingPayload), PayloadToJSON(merged))
						batch.Set(entry.key[:], val, nil)
						count++
					} else {
						skipped++
					}
				} else {
					skipped++
				}
			}

			newIdx++
			dbValid = iter.Next()
		}

		if newIdx%10000 == 0 {
			bar2.SetCurrent(int64(newIdx))
		}

		if count > 0 && count%int64(BatchSize) == 0 {
			if err := commitBatchSilent(batch); err != nil {
				bar2.Finish()
				return count, skipped, err
			}
		}
	}

	bar2.SetCurrent(int64(len(asnEntries)))
	bar2.Finish()

	NekoSection("阶段 3/3: 提交变更")

	if err := batch.Commit(pebble.NoSync); err != nil {
		return count, skipped, fmt.Errorf("提交最终批次失败: %v", err)
	}

	NekoSuccess(fmt.Sprintf("变更已提交，更新 %d 条，跳过 %d 条", count, skipped))

	return count, skipped, nil
}

func commitBatchSilent(batch *pebble.Batch) error {
	if err := batch.Commit(pebble.NoSync); err != nil {
		return fmt.Errorf("提交批次失败: %v", err)
	}
	batch.Reset()
	return nil
}