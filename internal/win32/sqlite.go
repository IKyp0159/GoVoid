//go:build windows

package win32

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"unicode/utf16"
)

const (
	sqliteMagic = "SQLite format 3\x00"

	pageInteriorTable = 0x05
	pageLeafTable     = 0x0D

	maxBTreedepth = 20
)

type SQLiteValue struct {
	Kind  byte
	Int   int64
	Float float64
	Text  string
	Blob  []byte
}

type SQLiteDB struct {
	data      []byte
	pageSize  int
	usable    int
	encoding  int
	walFrames int
}

func (db *SQLiteDB) WALFrames() int { return db.walFrames }

type sqliteTable struct {
	RootPage uint32
	SQL      string
}

func OpenSQLite(path string) (*SQLiteDB, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < 100 || string(data[:16]) != sqliteMagic {
		return nil, errors.New("sqlite: cabecera inválida")
	}

	pageSize := int(binary.BigEndian.Uint16(data[16:18]))
	if pageSize == 1 {
		pageSize = 65536
	}
	if pageSize < 512 || pageSize > 65536 || pageSize&(pageSize-1) != 0 {
		return nil, fmt.Errorf("sqlite: tamaño de página inválido: %d", pageSize)
	}

	reserved := int(data[20])
	usable := pageSize - reserved
	if usable < 480 {
		return nil, fmt.Errorf("sqlite: espacio usable inválido: %d", usable)
	}
	if len(data)%pageSize != 0 && len(data) < pageSize {
		return nil, errors.New("sqlite: archivo truncado")
	}

	encoding := int(binary.BigEndian.Uint32(data[56:60]))

	db := &SQLiteDB{
		data:     data,
		pageSize: pageSize,
		usable:   usable,
		encoding: encoding,
	}

	_ = db.applyWAL(path + "-wal")

	return db, nil
}

func (db *SQLiteDB) applyWAL(walPath string) error {
	wal, err := os.ReadFile(walPath)
	if err != nil {
		return err
	}
	if len(wal) < 32+24+db.pageSize {
		return nil
	}

	magic := binary.BigEndian.Uint32(wal[0:4])
	if magic != 0x377f0682 && magic != 0x377f0683 {
		return nil
	}
	if int(binary.BigEndian.Uint32(wal[8:12])) != db.pageSize {
		return nil
	}

	salt1 := binary.BigEndian.Uint32(wal[16:20])
	salt2 := binary.BigEndian.Uint32(wal[20:24])

	frameSize := 24 + db.pageSize
	applied := 0
	for offset := 32; offset+frameSize <= len(wal); offset += frameSize {
		pageNo := binary.BigEndian.Uint32(wal[offset : offset+4])
		if pageNo == 0 {
			break
		}
		if binary.BigEndian.Uint32(wal[offset+8:offset+12]) != salt1 ||
			binary.BigEndian.Uint32(wal[offset+12:offset+16]) != salt2 {
			break
		}

		start := (int(pageNo) - 1) * db.pageSize
		if start < 0 {
			break
		}
		if start+db.pageSize > len(db.data) {
			grown := make([]byte, start+db.pageSize)
			copy(grown, db.data)
			db.data = grown
		}
		copy(db.data[start:start+db.pageSize], wal[offset+24:offset+frameSize])
		applied++
	}

	if applied > 0 {
		db.walFrames = applied
	}
	return nil
}

func (db *SQLiteDB) pageCount() int {
	return len(db.data) / db.pageSize
}

func (db *SQLiteDB) page(no uint32) ([]byte, error) {
	if no == 0 {
		return nil, errors.New("sqlite: página 0")
	}
	start := (int(no) - 1) * db.pageSize
	if start < 0 || start+db.pageSize > len(db.data) {
		return nil, fmt.Errorf("sqlite: página %d fuera de rango", no)
	}
	return db.data[start : start+db.pageSize], nil
}

func readVarint(buf []byte, offset int) (uint64, int, error) {
	var value uint64
	for index := 0; index < 9; index++ {
		if offset+index >= len(buf) {
			return 0, 0, errors.New("sqlite: varint truncado")
		}
		current := buf[offset+index]
		if index == 8 {
			return (value << 8) | uint64(current), 9, nil
		}
		value = (value << 7) | uint64(current&0x7F)
		if current&0x80 == 0 {
			return value, index + 1, nil
		}
	}
	return 0, 0, errors.New("sqlite: varint inválido")
}

func (db *SQLiteDB) Tables() (map[string]sqliteTable, error) {
	rows, err := db.ReadTable(1, 512)
	if err != nil {
		return nil, err
	}

	tables := make(map[string]sqliteTable, len(rows))
	for _, row := range rows {
		if len(row) < 5 {
			continue
		}
		if row[0].Kind != 't' || row[0].Text != "table" {
			continue
		}
		name := row[1].Text
		root := uint32(row[3].Int)
		if name == "" || root == 0 {
			continue
		}
		tables[name] = sqliteTable{RootPage: root, SQL: row[4].Text}
	}
	return tables, nil
}

func (db *SQLiteDB) ReadTable(rootPage uint32, maxRows int) ([][]SQLiteValue, error) {
	if maxRows <= 0 {
		maxRows = 4096
	}
	rows := make([][]SQLiteValue, 0, 64)
	visited := make(map[uint32]bool, 32)

	var walk func(pageNo uint32, depth int) error
	walk = func(pageNo uint32, depth int) error {
		if depth > maxBTreedepth {
			return errors.New("sqlite: árbol demasiado profundo")
		}
		if visited[pageNo] {
			return fmt.Errorf("sqlite: ciclo en la página %d", pageNo)
		}
		visited[pageNo] = true

		buf, err := db.page(pageNo)
		if err != nil {
			return err
		}

		base := 0
		if pageNo == 1 {
			base = 100
		}
		if base+8 > len(buf) {
			return errors.New("sqlite: página truncada")
		}

		pageType := buf[base]
		cellCount := int(binary.BigEndian.Uint16(buf[base+3 : base+5]))

		headerSize := 8
		if pageType == pageInteriorTable {
			headerSize = 12
		}
		cellStart := base + headerSize
		if cellStart+cellCount*2 > len(buf) {
			return errors.New("sqlite: punteros de celda fuera de rango")
		}

		switch pageType {
		case pageInteriorTable:
			rightMost := binary.BigEndian.Uint32(buf[base+8 : base+12])
			for index := 0; index < cellCount; index++ {
				cellOffset := int(binary.BigEndian.Uint16(buf[cellStart+index*2 : cellStart+index*2+2]))
				if cellOffset+4 > len(buf) {
					continue
				}
				child := binary.BigEndian.Uint32(buf[cellOffset : cellOffset+4])
				if err := walk(child, depth+1); err != nil {
					return err
				}
				if len(rows) >= maxRows {
					return nil
				}
			}
			return walk(rightMost, depth+1)

		case pageLeafTable:
			for index := 0; index < cellCount; index++ {
				if len(rows) >= maxRows {
					return nil
				}
				cellOffset := int(binary.BigEndian.Uint16(buf[cellStart+index*2 : cellStart+index*2+2]))
				payload, err := db.readLeafPayload(buf, cellOffset)
				if err != nil {
					continue
				}
				values, err := db.decodeRecord(payload)
				if err != nil {
					continue
				}
				rows = append(rows, values)
			}
			return nil

		default:
			return fmt.Errorf("sqlite: tipo de página no soportado 0x%02x", pageType)
		}
	}

	if err := walk(rootPage, 0); err != nil {
		return nil, err
	}
	return rows, nil
}

func (db *SQLiteDB) readLeafPayload(buf []byte, cellOffset int) ([]byte, error) {
	if cellOffset < 0 || cellOffset >= len(buf) {
		return nil, errors.New("sqlite: celda fuera de rango")
	}

	payloadLen, n1, err := readVarint(buf, cellOffset)
	if err != nil {
		return nil, err
	}
	_, n2, err := readVarint(buf, cellOffset+n1)
	if err != nil {
		return nil, err
	}

	start := cellOffset + n1 + n2
	total := int(payloadLen)
	if total < 0 || total > 64*1024*1024 {
		return nil, fmt.Errorf("sqlite: payload de tamaño absurdo: %d", total)
	}

	u := db.usable
	maxLocal := u - 35
	if total <= maxLocal {
		if start+total > len(buf) {
			return nil, errors.New("sqlite: payload truncado")
		}
		return buf[start : start+total], nil
	}

	minLocal := ((u-12)*32)/255 - 23
	local := minLocal + (total-minLocal)%(u-4)
	if local > maxLocal {
		local = minLocal
	}
	if start+local+4 > len(buf) {
		return nil, errors.New("sqlite: payload local truncado")
	}

	out := make([]byte, 0, total)
	out = append(out, buf[start:start+local]...)

	next := binary.BigEndian.Uint32(buf[start+local : start+local+4])
	for next != 0 && len(out) < total {
		overflow, err := db.page(next)
		if err != nil {
			return nil, err
		}
		next = binary.BigEndian.Uint32(overflow[0:4])
		chunk := u - 4
		if len(out)+chunk > total {
			chunk = total - len(out)
		}
		if chunk <= 0 || 4+chunk > len(overflow) {
			break
		}
		out = append(out, overflow[4:4+chunk]...)
	}

	if len(out) != total {
		return nil, fmt.Errorf("sqlite: payload incompleto (%d/%d)", len(out), total)
	}
	return out, nil
}

func (db *SQLiteDB) decodeRecord(payload []byte) ([]SQLiteValue, error) {
	headerLen, n, err := readVarint(payload, 0)
	if err != nil {
		return nil, err
	}
	if int(headerLen) > len(payload) {
		return nil, errors.New("sqlite: cabecera de registro fuera de rango")
	}

	serialTypes := make([]uint64, 0, 16)
	offset := n
	for offset < int(headerLen) {
		serial, used, err := readVarint(payload, offset)
		if err != nil {
			return nil, err
		}
		serialTypes = append(serialTypes, serial)
		offset += used
	}

	values := make([]SQLiteValue, 0, len(serialTypes))
	body := int(headerLen)
	for _, serial := range serialTypes {
		value, size, err := db.decodeValue(serial, payload, body)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
		body += size
	}
	return values, nil
}

var integerSizes = [6]int{1, 2, 3, 4, 6, 8}

func (db *SQLiteDB) decodeValue(serial uint64, payload []byte, offset int) (SQLiteValue, int, error) {
	need := func(count int) error {
		if offset < 0 || offset+count > len(payload) {
			return errors.New("sqlite: valor truncado")
		}
		return nil
	}

	switch {
	case serial == 0:
		return SQLiteValue{Kind: 'n'}, 0, nil
	case serial >= 1 && serial <= 6:
		size := integerSizes[serial-1]
		if err := need(size); err != nil {
			return SQLiteValue{}, 0, err
		}
		var value int64
		for index := 0; index < size; index++ {
			value = (value << 8) | int64(payload[offset+index])
		}
		shift := uint(64 - 8*size)
		value = value << shift >> shift
		return SQLiteValue{Kind: 'i', Int: value}, size, nil
	case serial == 7:
		if err := need(8); err != nil {
			return SQLiteValue{}, 0, err
		}
		bits := binary.BigEndian.Uint64(payload[offset : offset+8])
		return SQLiteValue{Kind: 'f', Float: math.Float64frombits(bits)}, 8, nil
	case serial == 8:
		return SQLiteValue{Kind: 'i', Int: 0}, 0, nil
	case serial == 9:
		return SQLiteValue{Kind: 'i', Int: 1}, 0, nil
	case serial >= 12 && serial%2 == 0:
		size := int((serial - 12) / 2)
		if err := need(size); err != nil {
			return SQLiteValue{}, 0, err
		}
		blob := make([]byte, size)
		copy(blob, payload[offset:offset+size])
		return SQLiteValue{Kind: 'b', Blob: blob}, size, nil
	case serial >= 13 && serial%2 == 1:
		size := int((serial - 13) / 2)
		if err := need(size); err != nil {
			return SQLiteValue{}, 0, err
		}
		raw := payload[offset : offset+size]
		return SQLiteValue{Kind: 't', Text: db.decodeText(raw)}, size, nil
	default:
		return SQLiteValue{}, 0, fmt.Errorf("sqlite: serial type no soportado %d", serial)
	}
}

func (db *SQLiteDB) decodeText(raw []byte) string {
	switch db.encoding {
	case 2:
		return decodeUTF16(raw, binary.LittleEndian)
	case 3:
		return decodeUTF16(raw, binary.BigEndian)
	default:
		return string(raw)
	}
}

func decodeUTF16(raw []byte, order binary.ByteOrder) string {
	units := make([]uint16, 0, len(raw)/2)
	for index := 0; index+1 < len(raw); index += 2 {
		units = append(units, order.Uint16(raw[index:index+2]))
	}
	return string(utf16.Decode(units))
}
