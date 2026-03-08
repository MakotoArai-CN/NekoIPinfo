package codec

import (
	"encoding/binary"
	"math"

	json "github.com/goccy/go-json"
)

const (
	FormatVersion byte = 0x01

	headerSize = 1

	flagCountry   byte = 0x01
	flagProvince  byte = 0x02
	flagCity      byte = 0x04
	flagISP       byte = 0x08
	flagLatitude  byte = 0x10
	flagLongitude byte = 0x20
)

type IPInfoRecord struct {
	Country   string
	Province  string
	City      string
	ISP       string
	Latitude  float32
	Longitude float32
}

func Encode(r *IPInfoRecord) []byte {
	var flags byte
	countryB := []byte(r.Country)
	provinceB := []byte(r.Province)
	cityB := []byte(r.City)
	ispB := []byte(r.ISP)

	totalSize := headerSize + 1

	if len(countryB) > 0 {
		flags |= flagCountry
		totalSize += 2 + len(countryB)
	}
	if len(provinceB) > 0 {
		flags |= flagProvince
		totalSize += 2 + len(provinceB)
	}
	if len(cityB) > 0 {
		flags |= flagCity
		totalSize += 2 + len(cityB)
	}
	if len(ispB) > 0 {
		flags |= flagISP
		totalSize += 2 + len(ispB)
	}
	if r.Latitude != 0 {
		flags |= flagLatitude
		totalSize += 4
	}
	if r.Longitude != 0 {
		flags |= flagLongitude
		totalSize += 4
	}

	buf := make([]byte, totalSize)
	pos := 0

	buf[pos] = FormatVersion
	pos++

	buf[pos] = flags
	pos++

	if flags&flagCountry != 0 {
		binary.LittleEndian.PutUint16(buf[pos:], uint16(len(countryB)))
		pos += 2
		copy(buf[pos:], countryB)
		pos += len(countryB)
	}
	if flags&flagProvince != 0 {
		binary.LittleEndian.PutUint16(buf[pos:], uint16(len(provinceB)))
		pos += 2
		copy(buf[pos:], provinceB)
		pos += len(provinceB)
	}
	if flags&flagCity != 0 {
		binary.LittleEndian.PutUint16(buf[pos:], uint16(len(cityB)))
		pos += 2
		copy(buf[pos:], cityB)
		pos += len(cityB)
	}
	if flags&flagISP != 0 {
		binary.LittleEndian.PutUint16(buf[pos:], uint16(len(ispB)))
		pos += 2
		copy(buf[pos:], ispB)
		pos += len(ispB)
	}
	if flags&flagLatitude != 0 {
		binary.LittleEndian.PutUint32(buf[pos:], math.Float32bits(r.Latitude))
		pos += 4
	}
	if flags&flagLongitude != 0 {
		binary.LittleEndian.PutUint32(buf[pos:], math.Float32bits(r.Longitude))
		pos += 4
	}

	return buf[:pos]
}

func Decode(data []byte) (*IPInfoRecord, error) {
	if len(data) < 2 {
		return decodeAsJSON(data)
	}

	if data[0] != FormatVersion {
		return decodeAsJSON(data)
	}

	r := &IPInfoRecord{}
	flags := data[1]
	pos := 2

	if flags&flagCountry != 0 {
		s, n := readString(data, pos)
		if n < 0 {
			return nil, errCorrupt
		}
		r.Country = s
		pos += n
	}
	if flags&flagProvince != 0 {
		s, n := readString(data, pos)
		if n < 0 {
			return nil, errCorrupt
		}
		r.Province = s
		pos += n
	}
	if flags&flagCity != 0 {
		s, n := readString(data, pos)
		if n < 0 {
			return nil, errCorrupt
		}
		r.City = s
		pos += n
	}
	if flags&flagISP != 0 {
		s, n := readString(data, pos)
		if n < 0 {
			return nil, errCorrupt
		}
		r.ISP = s
		pos += n
	}
	if flags&flagLatitude != 0 {
		if pos+4 > len(data) {
			return nil, errCorrupt
		}
		r.Latitude = math.Float32frombits(binary.LittleEndian.Uint32(data[pos:]))
		pos += 4
	}
	if flags&flagLongitude != 0 {
		if pos+4 > len(data) {
			return nil, errCorrupt
		}
		r.Longitude = math.Float32frombits(binary.LittleEndian.Uint32(data[pos:]))
		pos += 4
	}

	return r, nil
}

func readString(data []byte, pos int) (string, int) {
	if pos+2 > len(data) {
		return "", -1
	}
	length := int(binary.LittleEndian.Uint16(data[pos:]))
	pos += 2
	if pos+length > len(data) {
		return "", -1
	}
	return string(data[pos : pos+length]), 2 + length
}

type jsonCompat struct {
	Country   string `json:"country"`
	Province  string `json:"province"`
	City      string `json:"city"`
	ISP       string `json:"isp"`
	Latitude  string `json:"latitude"`
	Longitude string `json:"longitude"`
}

func decodeAsJSON(data []byte) (*IPInfoRecord, error) {
	var j jsonCompat
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, err
	}
	r := &IPInfoRecord{
		Country:  j.Country,
		Province: j.Province,
		City:     j.City,
		ISP:      j.ISP,
	}
	r.Latitude = parseFloat32(j.Latitude)
	r.Longitude = parseFloat32(j.Longitude)
	return r, nil
}

func parseFloat32(s string) float32 {
	if s == "" {
		return 0
	}
	var neg bool
	i := 0
	if i < len(s) && s[i] == '-' {
		neg = true
		i++
	}
	var intPart float64
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		intPart = intPart*10 + float64(s[i]-'0')
		i++
	}
	var fracPart float64
	if i < len(s) && s[i] == '.' {
		i++
		scale := 0.1
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			fracPart += float64(s[i]-'0') * scale
			scale *= 0.1
			i++
		}
	}
	val := intPart + fracPart
	if neg {
		val = -val
	}
	return float32(val)
}

var errCorrupt = &DecodeError{"corrupt binary record"}

type DecodeError struct {
	msg string
}

func (e *DecodeError) Error() string {
	return e.msg
}

func ToJSON(r *IPInfoRecord) []byte {
	j := jsonCompat{
		Country:   r.Country,
		Province:  r.Province,
		City:      r.City,
		ISP:       r.ISP,
		Latitude:  formatFloat32(r.Latitude),
		Longitude: formatFloat32(r.Longitude),
	}
	data, _ := json.Marshal(j)
	return data
}

func formatFloat32(f float32) string {
	if f == 0 {
		return ""
	}
	buf := make([]byte, 0, 16)
	if f < 0 {
		buf = append(buf, '-')
		f = -f
	}
	intPart := int64(f)
	fracPart := f - float32(intPart)

	if intPart == 0 {
		buf = append(buf, '0')
	} else {
		tmp := make([]byte, 0, 10)
		v := intPart
		for v > 0 {
			tmp = append(tmp, byte('0'+v%10))
			v /= 10
		}
		for i := len(tmp) - 1; i >= 0; i-- {
			buf = append(buf, tmp[i])
		}
	}

	if fracPart > 0.0000005 {
		buf = append(buf, '.')
		for d := 0; d < 6; d++ {
			fracPart *= 10
			digit := int(fracPart)
			buf = append(buf, byte('0'+digit))
			fracPart -= float32(digit)
		}
		end := len(buf)
		for end > 0 && buf[end-1] == '0' {
			end--
		}
		if end > 0 && buf[end-1] == '.' {
			end--
		}
		buf = buf[:end]
	}
	return string(buf)
}

func IsLegacyJSON(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	return data[0] == '{'
}

func DecodeAuto(data []byte) (*IPInfoRecord, error) {
	if IsLegacyJSON(data) {
		return decodeAsJSON(data)
	}
	return Decode(data)
}

func EncodeFromJSON(jsonData []byte) ([]byte, error) {
	r, err := decodeAsJSON(jsonData)
	if err != nil {
		return nil, err
	}
	return Encode(r), nil
}

func RecordFromFields(country, province, city, isp, latitude, longitude string) *IPInfoRecord {
	return &IPInfoRecord{
		Country:   country,
		Province:  province,
		City:      city,
		ISP:       isp,
		Latitude:  parseFloat32(latitude),
		Longitude: parseFloat32(longitude),
	}
}

func FormatFloat32Str(f float32) string {
	return formatFloat32(f)
}