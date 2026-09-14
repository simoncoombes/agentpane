package installer

// ojson.go — JSON that remembers the order its keys were written in.
//
// This exists for one reason: the file being edited is the user's, and the
// only acceptable diff is the one the change actually makes. encoding/json
// round-trips an object through a Go map, which sorts keys alphabetically, so
// a single appended hook would rewrite every line of a settings file that was
// not already in alphabetical order. The bash installer has never done that —
// Python dictionaries keep insertion order — and the Windows path must not
// start.
//
// The same care covers two smaller things the diff would otherwise show:
// numbers keep their original text (1e3 does not become 1000, and a large
// integer does not round through float64), and the file's own indent unit is
// reused so a four-space or tab-indented file is not reformatted throughout.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type jkind int

const (
	jScalar jkind = iota
	jObject
	jArray
)

// jvalue is one JSON value. Objects keep their key order; scalars keep their
// source text.
type jvalue struct {
	kind  jkind
	tok   any // jScalar: string, json.Number, bool, or nil
	keys  []string
	field map[string]*jvalue
	items []*jvalue
}

func (v *jvalue) isObject() bool { return v != nil && v.kind == jObject }
func (v *jvalue) isArray() bool  { return v != nil && v.kind == jArray }

// get returns the named field, or nil when the value is not an object or has
// no such field.
func (v *jvalue) get(key string) *jvalue {
	if !v.isObject() {
		return nil
	}
	return v.field[key]
}

// set adds or replaces a field, appending to the key order when it is new so
// that an added key lands at the end rather than in the middle of the file.
func (v *jvalue) set(key string, val *jvalue) {
	if v.field == nil {
		v.field = map[string]*jvalue{}
	}
	if _, ok := v.field[key]; !ok {
		v.keys = append(v.keys, key)
	}
	v.field[key] = val
}

// del removes a field and its place in the key order.
func (v *jvalue) del(key string) {
	if !v.isObject() {
		return
	}
	if _, ok := v.field[key]; !ok {
		return
	}
	delete(v.field, key)
	for i, k := range v.keys {
		if k == key {
			v.keys = append(v.keys[:i], v.keys[i+1:]...)
			break
		}
	}
}

// str returns a scalar's string value, and "" for anything else.
func (v *jvalue) str() string {
	if v == nil || v.kind != jScalar {
		return ""
	}
	s, _ := v.tok.(string)
	return s
}

func newObject() *jvalue { return &jvalue{kind: jObject, field: map[string]*jvalue{}} }
func newArray() *jvalue  { return &jvalue{kind: jArray} }
func newString(s string) *jvalue {
	return &jvalue{kind: jScalar, tok: s}
}
func newNumber(n int) *jvalue {
	return &jvalue{kind: jScalar, tok: json.Number(fmt.Sprintf("%d", n))}
}

// parseJSON reads a complete JSON document.
//
// A duplicate key is an error rather than a last-one-wins merge: silently
// keeping one of two values would drop data the file visibly contains, and
// the rewrite would make that loss permanent. The bash installer refuses the
// same case for the same reason.
func parseJSON(data []byte) (*jvalue, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber() // keep a number's source text, not its float64 value
	v, err := parseValue(dec)
	if err != nil {
		return nil, err
	}
	// Anything after the top-level value is malformed, and a file we do not
	// fully understand is a file we must not rewrite.
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected trailing content after the top-level value")
	}
	return v, nil
}

func parseValue(dec *json.Decoder) (*jvalue, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	return parseFrom(dec, tok)
}

func parseFrom(dec *json.Decoder, tok json.Token) (*jvalue, error) {
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := newObject()
			for {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				if d, ok := kt.(json.Delim); ok && d == '}' {
					return obj, nil
				}
				key, ok := kt.(string)
				if !ok {
					return nil, fmt.Errorf("object key is not a string")
				}
				if _, dup := obj.field[key]; dup {
					return nil, fmt.Errorf("duplicate key %q", key)
				}
				val, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				obj.set(key, val)
			}
		case '[':
			arr := newArray()
			for {
				it, err := dec.Token()
				if err != nil {
					return nil, err
				}
				if d, ok := it.(json.Delim); ok && d == ']' {
					return arr, nil
				}
				val, err := parseFrom(dec, it)
				if err != nil {
					return nil, err
				}
				arr.items = append(arr.items, val)
			}
		}
		return nil, fmt.Errorf("unexpected delimiter %v", t)
	default:
		return &jvalue{kind: jScalar, tok: tok}, nil
	}
}

// render writes the document back out with the given indent unit, in the
// shape Python's json.dumps(indent=...) produces — which is what the bash
// installer has always written, so the two paths cannot drift into
// whole-file reformatting diffs against each other.
func (v *jvalue) render(indent string) string {
	var b strings.Builder
	v.write(&b, indent, "")
	b.WriteString("\n")
	return b.String()
}

func (v *jvalue) write(b *strings.Builder, indent, cur string) {
	switch v.kind {
	case jObject:
		if len(v.keys) == 0 {
			b.WriteString("{}")
			return
		}
		inner := cur + indent
		b.WriteString("{\n")
		for i, k := range v.keys {
			if i > 0 {
				b.WriteString(",\n")
			}
			b.WriteString(inner)
			writeJSONString(b, k)
			b.WriteString(": ")
			v.field[k].write(b, indent, inner)
		}
		b.WriteString("\n" + cur + "}")
	case jArray:
		if len(v.items) == 0 {
			b.WriteString("[]")
			return
		}
		inner := cur + indent
		b.WriteString("[\n")
		for i, it := range v.items {
			if i > 0 {
				b.WriteString(",\n")
			}
			b.WriteString(inner)
			it.write(b, indent, inner)
		}
		b.WriteString("\n" + cur + "]")
	default:
		switch t := v.tok.(type) {
		case nil:
			b.WriteString("null")
		case bool:
			if t {
				b.WriteString("true")
			} else {
				b.WriteString("false")
			}
		case json.Number:
			b.WriteString(t.String())
		case string:
			writeJSONString(b, t)
		default:
			// Unreachable with UseNumber, but a wrong guess here would
			// corrupt the file, so say so rather than write something.
			b.WriteString("null")
		}
	}
}

// writeJSONString encodes a Go string as a JSON string WITHOUT the HTML
// escaping encoding/json applies by default. A settings file full of
// < where it used to say < is exactly the gratuitous diff this whole
// file exists to avoid, and Python's json.dumps does not do it either.
func writeJSONString(b *strings.Builder, s string) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		b.WriteString(`""`)
		return
	}
	b.WriteString(strings.TrimRight(buf.String(), "\n"))
}

// detectIndent reports the indent unit a document already uses: the
// whitespace before its first indented line. Two spaces when the file is new,
// empty or single-line.
func detectIndent(text string) string {
	i := strings.IndexAny(text, "{[")
	if i < 0 {
		return "  "
	}
	rest := text[i+1:]
	nl := strings.IndexByte(rest, '\n')
	if nl < 0 {
		return "  "
	}
	// Only whitespace may sit between the brace and the newline.
	if strings.TrimRight(rest[:nl], " \t\r") != "" {
		return "  "
	}
	line := rest[nl+1:]
	unit := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
	if unit == "" {
		return "  "
	}
	return unit
}
