// Package ipp is a small Internet Printing Protocol codec (RFC 8010/8011): enough to
// print a document, follow the job, and read a printer's state and capabilities. It is
// shared by the server's ipp driver, discovery, and the virtual printer.
package ipp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Operations.
const (
	OpPrintJob             uint16 = 0x0002
	OpValidateJob          uint16 = 0x0004
	OpCancelJob            uint16 = 0x0008
	OpGetJobAttributes     uint16 = 0x0009
	OpGetPrinterAttributes uint16 = 0x000b
)

// Status codes (the subset CPS acts on; see StatusClass for the rest).
const (
	StatusOK                        uint16 = 0x0000
	StatusOKIgnoredOrSubstituted    uint16 = 0x0001
	StatusClientBadRequest          uint16 = 0x0400
	StatusClientNotFound            uint16 = 0x0406
	StatusDocumentFormatUnsupported uint16 = 0x040a
	StatusServerInternal            uint16 = 0x0500
	StatusServerNotAcceptingJobs    uint16 = 0x0506
	StatusServerBusy                uint16 = 0x0507
)

// Delimiter tags start an attribute group.
const (
	TagOperation   byte = 0x01
	TagJob         byte = 0x02
	TagEnd         byte = 0x03
	TagPrinter     byte = 0x04
	TagUnsupported byte = 0x05
)

// Value tags.
const (
	TagUnsupportedValue byte = 0x10
	TagUnknown          byte = 0x12
	TagNoValue          byte = 0x13
	TagInteger          byte = 0x21
	TagBoolean          byte = 0x22
	TagEnum             byte = 0x23
	TagOctetString      byte = 0x30
	TagDateTime         byte = 0x31
	TagResolution       byte = 0x32
	TagRange            byte = 0x33
	TagBeginCollection  byte = 0x34
	TagTextLang         byte = 0x35
	TagNameLang         byte = 0x36
	TagEndCollection    byte = 0x37
	TagText             byte = 0x41
	TagName             byte = 0x42
	TagKeyword          byte = 0x44
	TagURI              byte = 0x45
	TagURIScheme        byte = 0x46
	TagCharset          byte = 0x47
	TagLanguage         byte = 0x48
	TagMimeType         byte = 0x49
	TagMemberName       byte = 0x4a
)

// Value is one attribute value: an integer/enum/boolean in Int, anything textual in
// Str. Collections are not decoded (CPS needs none of them); their members are skipped.
type Value struct {
	Tag byte
	Int int
	Str string
}

// Attribute is a named, possibly multi-valued attribute.
type Attribute struct {
	Name   string
	Values []Value
}

// Group is one attribute group of a message.
type Group struct {
	Tag   byte
	Attrs []Attribute
}

// Get returns the named attribute in the group.
func (g *Group) Get(name string) (Attribute, bool) {
	for _, a := range g.Attrs {
		if a.Name == name {
			return a, true
		}
	}
	return Attribute{}, false
}

// Message is a request or a response. Code is the operation id in a request and the
// status code in a response. Data is the document that follows the attributes.
type Message struct {
	Major, Minor byte
	Code         uint16
	RequestID    uint32
	Groups       []Group
	Data         []byte
}

// NewRequest starts a request with the operation attributes every request carries.
func NewRequest(op uint16, id uint32, printerURI string) *Message {
	m := &Message{Major: 2, Minor: 0, Code: op, RequestID: id}
	m.Add(TagOperation, "attributes-charset", Value{Tag: TagCharset, Str: "utf-8"})
	m.Add(TagOperation, "attributes-natural-language", Value{Tag: TagLanguage, Str: "en"})
	if printerURI != "" {
		m.Add(TagOperation, "printer-uri", Value{Tag: TagURI, Str: printerURI})
	}
	return m
}

// Add appends an attribute to the group with that tag, creating the group if needed.
// Groups keep their first-use order, as RFC 8010 wants operation attributes first.
func (m *Message) Add(group byte, name string, values ...Value) *Message {
	for i := range m.Groups {
		if m.Groups[i].Tag == group {
			m.Groups[i].Attrs = append(m.Groups[i].Attrs, Attribute{Name: name, Values: values})
			return m
		}
	}
	m.Groups = append(m.Groups, Group{Tag: group, Attrs: []Attribute{{Name: name, Values: values}}})
	return m
}

// Group returns the first group with tag, or nil.
func (m *Message) Group(tag byte) *Group {
	for i := range m.Groups {
		if m.Groups[i].Tag == tag {
			return &m.Groups[i]
		}
	}
	return nil
}

// Find returns the named attribute from the first group that has it.
func (m *Message) Find(name string) (Attribute, bool) {
	for i := range m.Groups {
		if a, ok := m.Groups[i].Get(name); ok {
			return a, true
		}
	}
	return Attribute{}, false
}

// Int returns the first value of an integer or enum attribute.
func (m *Message) Int(name string) (int, bool) {
	a, ok := m.Find(name)
	if !ok || len(a.Values) == 0 {
		return 0, false
	}
	return a.Values[0].Int, true
}

// String returns the first value of a textual attribute.
func (m *Message) String(name string) string {
	a, ok := m.Find(name)
	if !ok || len(a.Values) == 0 {
		return ""
	}
	return a.Values[0].Str
}

// Strings returns every value of a textual attribute.
func (m *Message) Strings(name string) []string {
	a, _ := m.Find(name)
	out := make([]string, 0, len(a.Values))
	for _, v := range a.Values {
		out = append(out, v.Str)
	}
	return out
}

// Integer, Keyword, Name, Text, MimeType, Enum and Bool build values.
func Integer(n int) Value     { return Value{Tag: TagInteger, Int: n} }
func Enum(n int) Value        { return Value{Tag: TagEnum, Int: n} }
func Keyword(s string) Value  { return Value{Tag: TagKeyword, Str: s} }
func Name(s string) Value     { return Value{Tag: TagName, Str: s} }
func Text(s string) Value     { return Value{Tag: TagText, Str: s} }
func MimeType(s string) Value { return Value{Tag: TagMimeType, Str: s} }
func URI(s string) Value      { return Value{Tag: TagURI, Str: s} }
func Bool(b bool) Value {
	if b {
		return Value{Tag: TagBoolean, Int: 1}
	}
	return Value{Tag: TagBoolean}
}

func isIntTag(t byte) bool { return t == TagInteger || t == TagEnum }

// Encode writes the message: header, groups, end tag, then the document.
func (m *Message) Encode() []byte {
	var b bytes.Buffer
	b.WriteByte(m.Major)
	b.WriteByte(m.Minor)
	_ = binary.Write(&b, binary.BigEndian, m.Code)
	_ = binary.Write(&b, binary.BigEndian, m.RequestID)
	for _, g := range m.Groups {
		b.WriteByte(g.Tag)
		for _, a := range g.Attrs {
			for i, v := range a.Values {
				b.WriteByte(v.Tag)
				name := a.Name
				if i > 0 {
					name = "" // additional value
				}
				_ = binary.Write(&b, binary.BigEndian, uint16(len(name)))
				b.WriteString(name)
				switch {
				case isIntTag(v.Tag):
					_ = binary.Write(&b, binary.BigEndian, uint16(4))
					_ = binary.Write(&b, binary.BigEndian, int32(v.Int))
				case v.Tag == TagBoolean:
					_ = binary.Write(&b, binary.BigEndian, uint16(1))
					b.WriteByte(byte(v.Int))
				case v.Tag == TagNoValue || v.Tag == TagUnknown || v.Tag == TagUnsupportedValue:
					_ = binary.Write(&b, binary.BigEndian, uint16(0))
				default:
					_ = binary.Write(&b, binary.BigEndian, uint16(len(v.Str)))
					b.WriteString(v.Str)
				}
			}
		}
	}
	b.WriteByte(TagEnd)
	b.Write(m.Data)
	return b.Bytes()
}

// ErrMalformed is returned for a message that does not parse.
var ErrMalformed = errors.New("malformed IPP message")

// Decode parses a message. Whatever follows the end-of-attributes tag is Data.
func Decode(raw []byte) (*Message, error) {
	r := bytes.NewReader(raw)
	var hdr struct {
		Major, Minor byte
		Code         uint16
		RequestID    uint32
	}
	if err := binary.Read(r, binary.BigEndian, &hdr); err != nil {
		return nil, fmt.Errorf("%w: header: %v", ErrMalformed, err)
	}
	m := &Message{Major: hdr.Major, Minor: hdr.Minor, Code: hdr.Code, RequestID: hdr.RequestID}
	var group *Group
	var last *Attribute
	depth := 0 // inside a collection: members are skipped
	for {
		tag, err := r.ReadByte()
		if err != nil {
			return nil, fmt.Errorf("%w: missing end-of-attributes", ErrMalformed)
		}
		if tag == TagEnd {
			break
		}
		if tag < 0x10 { // delimiter: a new group
			m.Groups = append(m.Groups, Group{Tag: tag})
			group, last = &m.Groups[len(m.Groups)-1], nil
			continue
		}
		name, err := readString(r)
		if err != nil {
			return nil, err
		}
		val, err := readString(r)
		if err != nil {
			return nil, err
		}
		switch tag {
		case TagBeginCollection:
			depth++
			if depth == 1 && group != nil && name != "" {
				group.Attrs = append(group.Attrs, Attribute{Name: name, Values: []Value{{Tag: tag}}})
				last = &group.Attrs[len(group.Attrs)-1]
			}
			continue
		case TagEndCollection:
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth > 0 || group == nil {
			continue
		}
		v := Value{Tag: tag}
		switch {
		case isIntTag(tag) && len(val) == 4:
			v.Int = int(int32(binary.BigEndian.Uint32([]byte(val))))
		case tag == TagBoolean && len(val) == 1:
			v.Int = int(val[0])
		case tag == TagTextLang || tag == TagNameLang:
			v.Str = langString(val)
		default:
			v.Str = val
		}
		if name == "" && last != nil {
			last.Values = append(last.Values, v)
			continue
		}
		group.Attrs = append(group.Attrs, Attribute{Name: name, Values: []Value{v}})
		last = &group.Attrs[len(group.Attrs)-1]
	}
	m.Data, _ = io.ReadAll(r)
	return m, nil
}

func readString(r *bytes.Reader) (string, error) {
	var n uint16
	if err := binary.Read(r, binary.BigEndian, &n); err != nil {
		return "", fmt.Errorf("%w: length: %v", ErrMalformed, err)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", fmt.Errorf("%w: value: %v", ErrMalformed, err)
	}
	return string(b), nil
}

// langString extracts the text of a textWithLanguage/nameWithLanguage value:
// a language and the text, each with a two-byte length.
func langString(v string) string {
	b := []byte(v)
	if len(b) < 2 {
		return ""
	}
	n := int(binary.BigEndian.Uint16(b))
	if len(b) < 2+n+2 {
		return ""
	}
	b = b[2+n:]
	m := int(binary.BigEndian.Uint16(b))
	if len(b) < 2+m {
		return ""
	}
	return string(b[2 : 2+m])
}

// StatusClass is the class of a status code: "successful", "informational",
// "redirection", "client-error" or "server-error".
func StatusClass(code uint16) string {
	switch code >> 8 {
	case 0x00:
		return "successful"
	case 0x01:
		return "informational"
	case 0x02:
		return "redirection"
	case 0x04:
		return "client-error"
	default:
		return "server-error"
	}
}
