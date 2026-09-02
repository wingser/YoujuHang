// Package proto 提供 protobuf 最小编解码（仅支持 varint / len-delimited / fixed64，足够覆盖游聚协议）
package proto

// AppendVarint 追加 varint 编码
func AppendVarint(dst []byte, v uint64) []byte {
	for v >= 0x80 {
		dst = append(dst, byte(v)|0x80)
		v >>= 7
	}
	return append(dst, byte(v))
}

// FieldVarint 编码 field 的 varint 字段
func FieldVarint(dst []byte, field int, v uint64) []byte {
	dst = AppendVarint(dst, uint64(field)<<3)
	return AppendVarint(dst, v)
}

// FieldBytes 编码 field 的 bytes 字段
func FieldBytes(dst []byte, field int, b []byte) []byte {
	dst = AppendVarint(dst, uint64(field)<<3|2)
	dst = AppendVarint(dst, uint64(len(b)))
	return append(dst, b...)
}

// FieldString 编码 field 的 string 字段
func FieldString(dst []byte, field int, s string) []byte {
	return FieldBytes(dst, field, []byte(s))
}

// FieldFixed64 编码 field 的 fixed64 字段
func FieldFixed64(dst []byte, field int, v uint64) []byte {
	dst = AppendVarint(dst, uint64(field)<<3|1)
	for i := 0; i < 8; i++ {
		dst = append(dst, byte(v>>(8*i)))
	}
	return dst
}

// ReadVarint 从 off 读 varint，返回 (值, 新偏移)
func ReadVarint(b []byte, off int) (uint64, int) {
	var v uint64
	var shift uint
	for {
		x := b[off]
		off++
		v |= uint64(x&0x7F) << shift
		if x&0x80 == 0 {
			return v, off
		}
		shift += 7
	}
}

// Field 是解码后的一个 protobuf 字段
type Field struct {
	Num  int    // 字段号
	Wire int    // wire type
	V    uint64 // varint 值（wire=0）或 fixed64 低 8 字节
	B    []byte // bytes 值（wire=2）
}

// Parse 解析 protobuf 字节流为字段列表
func Parse(b []byte) []Field {
	var fields []Field
	off := 0
	for off < len(b) {
		tag, o := ReadVarint(b, off)
		if o > len(b) {
			break
		}
		off = o
		f := Field{Num: int(tag >> 3), Wire: int(tag & 7)}
		switch f.Wire {
		case 0:
			v, o := ReadVarint(b, off)
			if o > len(b) {
				break
			}
			f.V = v
			off = o
		case 1:
			if off+8 > len(b) {
				break
			}
			f.V = 0
			for i := 0; i < 8; i++ {
				f.V |= uint64(b[off+i]) << (8 * i)
			}
			off += 8
		case 2:
			l, o := ReadVarint(b, off)
			if o > len(b) || o+int(l) > len(b) {
				break
			}
			f.B = b[o : o+int(l)]
			off = o + int(l)
		case 5:
			if off+4 > len(b) {
				break
			}
			f.B = b[off : off+4]
			off += 4
		case 3:
			// skip group start: read until matching group end
			depth := 1
			for depth > 0 && off < len(b) {
				if off >= len(b) {
					break
				}
				t2, o2 := ReadVarint(b, off)
				if o2 > len(b) {
					break
				}
				off = o2
				w2 := t2 & 7
				if w2 == 3 {
					depth++
				} else if w2 == 4 {
					depth--
				} else if w2 == 0 {
					_, off = ReadVarint(b, off)
				} else if w2 == 1 {
					off += 8
				} else if w2 == 2 {
					l2, o3 := ReadVarint(b, off)
					if o3 > len(b) {
						break
					}
					off = o3 + int(l2)
				} else if w2 == 5 {
					off += 4
				} else {
					break
				}
			}
			continue
		case 4:
			continue
		default:
			break
		}
		fields = append(fields, f)
	}
	return fields
}

// Get 取第 num 个字段（wire=2 返回 B，varint 返回 V）
func Get(fields []Field, num int) *Field {
	for i := range fields {
		if fields[i].Num == num {
			return &fields[i]
		}
	}
	return nil
}

// GetAll 取全部 num 字段（重复字段）
func GetAll(fields []Field, num int) []*Field {
	var out []*Field
	for i := range fields {
		if fields[i].Num == num {
			out = append(out, &fields[i])
		}
	}
	return out
}

// GetBytes 取 bytes 字段值（不存在返回 nil）
func GetBytes(fields []Field, num int) []byte {
	if f := Get(fields, num); f != nil && f.Wire == 2 {
		return f.B
	}
	return nil
}

// GetString 取 string 字段值
func GetString(fields []Field, num int) string {
	if b := GetBytes(fields, num); b != nil {
		return string(b)
	}
	return ""
}

// GetVarint 取 varint 字段值
func GetVarint(fields []Field, num int) uint64 {
	if f := Get(fields, num); f != nil && f.Wire == 0 {
		return f.V
	}
	return 0
}
