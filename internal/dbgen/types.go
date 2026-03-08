package dbgen

import (
	"fmt"
	"net"
	"strings"

	"github.com/Chocola-X/NekoIPinfo/internal/codec"
	json "github.com/goccy/go-json"
)

const (
	DefaultDBPath       = "ip_info"
	DefaultBackupDir    = "ip_info_backup"
	DefaultChangelogDir = "ip_info_changelog"

	DbipCityURLTemplate = "https://download.db-ip.com/free/dbip-city-lite-%s.mmdb.gz"
	DbipASNURLTemplate  = "https://download.db-ip.com/free/dbip-asn-lite-%s.mmdb.gz"

	BatchSize = 50000
)

type IPInfoFields struct {
	Country   string `json:"country"`
	Province  string `json:"province"`
	City      string `json:"city"`
	ISP       string `json:"isp"`
	Latitude  string `json:"latitude"`
	Longitude string `json:"longitude"`
}

type ChangelogEntry struct {
	Timestamp string `json:"ts"`
	Action    string `json:"action"`
	OldJSON   string `json:"old,omitempty"`
	NewJSON   string `json:"new"`
}

type MmdbRecord struct {
	Country struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"country"`
	Subdivisions []struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"subdivisions"`
	City struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
	Location struct {
		Latitude  float64 `maxminddb:"latitude"`
		Longitude float64 `maxminddb:"longitude"`
	} `maxminddb:"location"`
	Traits struct {
		ISP          string `maxminddb:"isp"`
		Organization string `maxminddb:"organization"`
	} `maxminddb:"traits"`
}

type AsnRecord struct {
	AutonomousSystemOrganization string `maxminddb:"autonomous_system_organization"`
	ISP                          string `maxminddb:"isp"`
	Organization                 string `maxminddb:"organization"`
}

func LastIPInNetwork(network *net.IPNet) net.IP {
	ip := network.IP.To16()
	if ip == nil {
		return nil
	}
	mask := network.Mask
	if len(mask) == 4 {
		m := make(net.IPMask, 16)
		copy(m[:12], []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
		copy(m[12:], mask)
		mask = m
	}
	broadcast := make(net.IP, 16)
	for i := 0; i < 16; i++ {
		broadcast[i] = ip[i] | ^mask[i]
	}
	return broadcast
}

func GetName(names map[string]string) string {
	if v, ok := names["zh-CN"]; ok && v != "" {
		return v
	}
	if v, ok := names["en"]; ok && v != "" {
		return v
	}
	for _, v := range names {
		if v != "" {
			return v
		}
	}
	return ""
}

func FloatToString(f float64) string {
	if f == 0 {
		return ""
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.6f", f), "0"), ".")
}

func MakeKey(ip net.IP) [16]byte {
	var key [16]byte
	b := ip.To16()
	if b != nil {
		copy(key[:], b)
	}
	return key
}

func MakeValue(endIP net.IP, payload []byte) []byte {
	end16 := endIP.To16()
	if end16 == nil {
		return nil
	}
	val := make([]byte, 16+len(payload))
	copy(val[:16], end16)
	copy(val[16:], payload)
	return val
}

func MakeValueFromRecord(endIP net.IP, r *codec.IPInfoRecord) []byte {
	encoded := codec.Encode(r)
	return MakeValue(endIP, encoded)
}

func FieldsToRecord(f *IPInfoFields) *codec.IPInfoRecord {
	return codec.RecordFromFields(f.Country, f.Province, f.City, f.ISP, f.Latitude, f.Longitude)
}

func EncodeFields(f *IPInfoFields) []byte {
	r := FieldsToRecord(f)
	return codec.Encode(r)
}

func DecodePayload(data []byte) (*IPInfoFields, error) {
	r, err := codec.DecodeAuto(data)
	if err != nil {
		return nil, err
	}
	return &IPInfoFields{
		Country:   r.Country,
		Province:  r.Province,
		City:      r.City,
		ISP:       r.ISP,
		Latitude:  codec.FormatFloat32Str(r.Latitude),
		Longitude: codec.FormatFloat32Str(r.Longitude),
	}, nil
}

func LookupASN(asnDB interface{ Lookup(net.IP, interface{}) error }, ip net.IP) string {
	if asnDB == nil {
		return ""
	}
	var record AsnRecord
	err := asnDB.Lookup(ip, &record)
	if err != nil {
		return ""
	}
	if record.ISP != "" {
		return record.ISP
	}
	if record.Organization != "" {
		return record.Organization
	}
	if record.AutonomousSystemOrganization != "" {
		return record.AutonomousSystemOrganization
	}
	return ""
}

func MergeFields(existingPayload, newPayload []byte, fieldsToUpdate []string) ([]byte, bool) {
	existing, err1 := DecodePayload(existingPayload)
	incoming, err2 := DecodePayload(newPayload)
	if err1 != nil {
		return newPayload, true
	}
	if err2 != nil {
		return existingPayload, false
	}

	changed := false
	for _, field := range fieldsToUpdate {
		switch field {
		case "country":
			if incoming.Country != "" && incoming.Country != existing.Country {
				existing.Country = incoming.Country
				changed = true
			}
		case "province":
			if incoming.Province != "" && incoming.Province != existing.Province {
				existing.Province = incoming.Province
				changed = true
			}
		case "city":
			if incoming.City != "" && incoming.City != existing.City {
				existing.City = incoming.City
				changed = true
			}
		case "isp":
			if incoming.ISP != "" && incoming.ISP != existing.ISP {
				existing.ISP = incoming.ISP
				changed = true
			}
		case "latitude":
			if incoming.Latitude != "" && incoming.Latitude != existing.Latitude {
				existing.Latitude = incoming.Latitude
				changed = true
			}
		case "longitude":
			if incoming.Longitude != "" && incoming.Longitude != existing.Longitude {
				existing.Longitude = incoming.Longitude
				changed = true
			}
		}
	}

	if !changed {
		return existingPayload, false
	}

	merged := EncodeFields(existing)
	return merged, true
}

func PayloadToJSON(data []byte) []byte {
	r, err := codec.DecodeAuto(data)
	if err != nil {
		return data
	}
	return codec.ToJSON(r)
}

func PayloadToJSONString(data []byte) string {
	return string(PayloadToJSON(data))
}

func JSONToPayload(jsonData []byte) []byte {
	encoded, err := codec.EncodeFromJSON(jsonData)
	if err != nil {
		return jsonData
	}
	return encoded
}

func MergeFieldsJSON(existingJSON, newJSON []byte, fieldsToUpdate []string) ([]byte, bool) {
	var existing IPInfoFields
	var incoming IPInfoFields
	if err := json.Unmarshal(existingJSON, &existing); err != nil {
		return newJSON, true
	}
	if err := json.Unmarshal(newJSON, &incoming); err != nil {
		return existingJSON, false
	}

	changed := false
	for _, field := range fieldsToUpdate {
		switch field {
		case "country":
			if incoming.Country != "" && incoming.Country != existing.Country {
				existing.Country = incoming.Country
				changed = true
			}
		case "province":
			if incoming.Province != "" && incoming.Province != existing.Province {
				existing.Province = incoming.Province
				changed = true
			}
		case "city":
			if incoming.City != "" && incoming.City != existing.City {
				existing.City = incoming.City
				changed = true
			}
		case "isp":
			if incoming.ISP != "" && incoming.ISP != existing.ISP {
				existing.ISP = incoming.ISP
				changed = true
			}
		case "latitude":
			if incoming.Latitude != "" && incoming.Latitude != existing.Latitude {
				existing.Latitude = incoming.Latitude
				changed = true
			}
		case "longitude":
			if incoming.Longitude != "" && incoming.Longitude != existing.Longitude {
				existing.Longitude = incoming.Longitude
				changed = true
			}
		}
	}

	if !changed {
		return existingJSON, false
	}

	merged, err := json.Marshal(existing)
	if err != nil {
		return existingJSON, false
	}
	return merged, true
}