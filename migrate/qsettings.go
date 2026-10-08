package migrate

import (
	"encoding/binary"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Reader for configuration files written by Qt's QSettings in IniFormat.
//
// The escaping rules mirror Qt's own iniUnescapedKey, iniUnescapedStringList
// and stringToVariant (qtbase/src/corelib/io/qsettings.cpp), which are not
// standard INI:
//   - group separators inside keys are written as `\` (`0\Folders\x\localPath`);
//   - other special key characters are percent-encoded (`%3A`, `%U00E9`);
//   - values use C-like escapes (`\n`, `\"`, `\xNNNN` UTF-16 code units, octal);
//   - double quotes protect commas, and unquoted commas make a string list;
//   - an unquoted `;` starts a comment, also in the middle of a line;
//   - `@Variant(...)` / `@ByteArray(...)` hold binary data, one byte per char.

type valueKind int

const (
	kindString valueKind = iota
	kindStringList
	kindByteArray
	kindVariant // a QDataStream-serialized QVariant
	kindInvalid
)

type value struct {
	kind  valueKind
	str   string
	list  []string
	bytes []byte
}

// url decodes a URL stored either as plain text or as a serialized QUrl
// variant: type id 17, then the encoded URL as a QByteArray (big-endian
// length, then the bytes).
func (v value) url() string {
	const qurlTypeID = 17
	switch v.kind {
	case kindString:
		return v.str
	case kindVariant:
		b := v.bytes
		if len(b) < 8 || binary.BigEndian.Uint32(b) != qurlTypeID {
			return ""
		}
		// 0xFFFFFFFF (a null byte array) is longer than any payload.
		n := uint64(binary.BigEndian.Uint32(b[4:]))
		if n > uint64(len(b)-8) {
			return ""
		}
		return string(b[8 : 8+n])
	}
	return ""
}

// settings are keyed by their full path, e.g. "Accounts/0/Folders/<id>/localPath";
// keys of the [General] section are kept under "General/".
type settings map[string]value

func (s settings) str(key string) string {
	if v := s[key]; v.kind == kindString {
		return v.str
	}
	return ""
}

// boolean interprets the value like QVariant::toBool, def when unset.
func (s settings) boolean(key string, def bool) bool {
	v, ok := s[key]
	if !ok || v.kind != kindString {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v.str)) {
	case "true", "1":
		return true
	case "false", "0", "":
		return false
	}
	return def
}

func parseSettings(text string) settings {
	out := settings{}
	section := ""
	for _, line := range logicalLines(strings.TrimPrefix(text, "\ufeff")) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			name := strings.TrimSuffix(strings.TrimLeft(line, "["), "]")
			section = unescapeKey(strings.TrimSpace(name))
			continue
		}
		key, raw, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = unescapeKey(strings.TrimSpace(key))
		if section != "" {
			key = section + "/" + key
		}
		out[key] = parseValue(strings.TrimSpace(raw))
	}
	return out
}

// logicalLines joins physical lines that end in an unescaped backslash,
// which Qt treats as a line continuation.
func logicalLines(text string) []string {
	var lines []string
	var cur strings.Builder
	for raw := range strings.SplitSeq(text, "\n") {
		raw = strings.TrimSuffix(raw, "\r")
		if trailing := len(raw) - len(strings.TrimRight(raw, `\`)); trailing%2 == 1 {
			cur.WriteString(raw[:len(raw)-1])
			continue
		}
		cur.WriteString(raw)
		lines = append(lines, cur.String())
		cur.Reset()
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	return lines
}

// unescapeKey reverses iniEscapedKey: `\` separates groups, `%XX` and
// `%UXXXX` encode any other character.
func unescapeKey(key string) string {
	var units []uint16
	for i := 0; i < len(key); {
		switch c := key[i]; c {
		case '\\':
			units = append(units, '/')
			i++
		case '%':
			digits, start := 2, i+1
			if start < len(key) && key[start] == 'U' {
				digits, start = 4, start+1
			}
			if start+digits <= len(key) {
				if v, err := strconv.ParseUint(key[start:start+digits], 16, 16); err == nil {
					units = append(units, uint16(v))
					i = start + digits
					continue
				}
			}
			units = append(units, '%')
			i++
		default:
			r, size := utf8.DecodeRuneInString(key[i:])
			units = utf16.AppendRune(units, r)
			i += size
		}
	}
	return string(utf16.Decode(units))
}

// parseValue reverses iniEscapedStringList and variantToString.
func parseValue(raw string) value {
	items, isList := unescapeStringList(raw)
	if isList {
		list := make([]string, len(items))
		for i, u := range items {
			list[i] = string(utf16.Decode(u))
		}
		return value{kind: kindStringList, list: list}
	}
	units := items[0]
	text := string(utf16.Decode(units))
	if rest, ok := strings.CutPrefix(text, "@"); ok {
		switch {
		case strings.HasPrefix(rest, "@"):
			return value{kind: kindString, str: rest}
		case rest == "Invalid()":
			return value{kind: kindInvalid}
		case strings.HasPrefix(rest, "String(") && strings.HasSuffix(rest, ")"):
			return value{kind: kindString, str: rest[len("String(") : len(rest)-1]}
		}
		// Binary payloads map one UTF-16 code unit to one byte (Latin-1),
		// up to the last ')'.
		for _, p := range []struct {
			prefix string
			kind   valueKind
		}{{"@ByteArray(", kindByteArray}, {"@Variant(", kindVariant}} {
			n := len(p.prefix)
			if strings.HasPrefix(text, p.prefix) && len(units) > n && units[len(units)-1] == ')' {
				b := make([]byte, 0, len(units)-n-1)
				for _, u := range units[n : len(units)-1] {
					b = append(b, byte(u))
				}
				return value{kind: p.kind, bytes: b}
			}
		}
	}
	return value{kind: kindString, str: text}
}

var simpleEscapes = map[uint16]uint16{
	'a': 0x07, 'b': 0x08, 'f': 0x0c, 'n': '\n', 'r': '\r', 't': '\t', 'v': 0x0b,
	'"': '"', '?': '?', '\'': '\'', '\\': '\\',
}

// digit returns the value of u as a digit in base 8 or 16.
func digit(u uint16, base uint32) (uint32, bool) {
	var d uint32
	switch {
	case u >= '0' && u <= '9':
		d = uint32(u - '0')
	case u >= 'a' && u <= 'f':
		d = uint32(u-'a') + 10
	case u >= 'A' && u <= 'F':
		d = uint32(u-'A') + 10
	default:
		return 0, false
	}
	return d, d < base
}

// unescapeStringList splits a raw value into items (as UTF-16 code units),
// applying escapes and quoting, and reports whether an unquoted comma made
// it a string list.
func unescapeStringList(raw string) (items [][]uint16, isList bool) {
	units := utf16.Encode([]rune(raw))
	var cur []uint16
	// Length of cur up to the last character that must survive trimming
	// (quoted or escaped); unquoted trailing spaces are dropped.
	keep := 0
	quoted := false
	isSpace := func(u uint16) bool { return u == ' ' || u == '\t' }
	i := 0
	skipSpaces := func() {
		for i < len(units) && isSpace(units[i]) {
			i++
		}
	}
	skipSpaces()

loop:
	for i < len(units) {
		u := units[i]
		switch {
		case u == '\\':
			i++
			if i >= len(units) {
				break loop
			}
			e := units[i]
			i++
			if m, ok := simpleEscapes[e]; ok {
				cur = append(cur, m)
				keep = len(cur)
				continue
			}
			var v uint32
			switch {
			case e == 'x':
				for i < len(units) {
					d, ok := digit(units[i], 16)
					if !ok {
						break
					}
					v = v<<4 | d
					i++
				}
			case e >= '0' && e <= '7':
				v = uint32(e - '0')
				for i < len(units) {
					d, ok := digit(units[i], 8)
					if !ok {
						break
					}
					v = v<<3 | d
					i++
				}
			default:
				// Unknown escapes are dropped, as in Qt.
				continue
			}
			cur = append(cur, uint16(v))
			keep = len(cur)
		case u == '"':
			quoted = !quoted
			keep = len(cur)
			i++
		case u == ';' && !quoted:
			break loop
		case u == ',' && !quoted:
			items = append(items, cur[:keep])
			cur, keep, isList = nil, 0, true
			i++
			skipSpaces()
		default:
			cur = append(cur, u)
			if quoted || !isSpace(u) {
				keep = len(cur)
			}
			i++
		}
	}
	return append(items, cur[:keep]), isList
}
