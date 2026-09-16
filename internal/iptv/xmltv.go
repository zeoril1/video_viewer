package iptv

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"time"
)

// EPGChannel — канал из XMLTV (сопоставляем по id и отображаемому имени).
type EPGChannel struct {
	ID      string
	Name    string
	Icon    string
	Aliases []string // display-name'ы и другие альтернативные имена
}

// Programme — передача из XMLTV.
type Programme struct {
	ChannelID string
	Start     time.Time
	Stop      time.Time
	Title     string
	Desc      string
	Category  string
}

// xmltvTimeLayouts — встречающиеся в XMLTV форматы времени: основной
// «20260910120000 +0300», но часть генераторов пишет без пробела или в UTC.
var xmltvTimeLayouts = []string{
	"20060102150405 -0700",
	"20060102150405-0700",
	"20060102150405",
}

// parseXMLTVTime разбирает время программы.
func parseXMLTVTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range xmltvTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// ParseXMLTV читает телепрограмму XMLTV и возвращает каналы и передачи;
// поток разбирается инкрементально (файлы бывают на десятки мегабайт).
func ParseXMLTV(r io.Reader) ([]EPGChannel, []Programme, error) {
	return ParseXMLTVFilter(r, nil)
}

// ParseXMLTVFilter — как ParseXMLTV, но передачи берутся только для каналов,
// одобренных want (nil — все): списки XMLTV целых стран весят десятки
// мегабайт. Элементы <channel> идут раньше <programme>, поэтому want может
// опираться на уже разобранные каналы.
func ParseXMLTVFilter(r io.Reader, want func(channelID string) bool) ([]EPGChannel, []Programme, error) {
	dec := xml.NewDecoder(r)
	dec.Strict = false
	dec.CharsetReader = charsetReader

	var (
		channels []EPGChannel
		programs []Programme
	)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("parse xmltv: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "channel":
			ch := EPGChannel{}
			for _, a := range se.Attr {
				if a.Name.Local == "id" {
					ch.ID = a.Value
				}
			}
			if err := decodeChannel(dec, &ch); err != nil {
				return nil, nil, err
			}
			ch.Name = strings.TrimSpace(ch.Name)
			if ch.ID == "" && ch.Name != "" {
				ch.ID = ch.Name
			}
			channels = append(channels, ch)
		case "programme":
			p := Programme{}
			for _, a := range se.Attr {
				switch a.Name.Local {
				case "start":
					if t, ok := parseXMLTVTime(a.Value); ok {
						p.Start = t
					}
				case "stop":
					if t, ok := parseXMLTVTime(a.Value); ok {
						p.Stop = t
					}
				case "channel":
					p.ChannelID = a.Value
				}
			}
			if err := decodeProgramme(dec, &p); err != nil {
				return nil, nil, err
			}
			// Без времени или канала передача бесполезна.
			if p.ChannelID == "" || p.Start.IsZero() {
				continue
			}
			if want != nil && !want(p.ChannelID) {
				continue
			}
			if p.Stop.IsZero() || p.Stop.Before(p.Start) {
				// Некоторые генераторы не пишут stop — считаем 1 час.
				p.Stop = p.Start.Add(time.Hour)
			}
			programs = append(programs, p)
		}
	}
	return channels, programs, nil
}

// decodeChannel дочитывает содержимое <channel> (display-name/icon).
//
// ВАЖНО: DecodeElement сам съедает свой закрывающий тег, поэтому счётчик
// глубины для него НЕ увеличиваем — иначе цикл уходит за </channel> и получает EOF.
func decodeChannel(dec *xml.Decoder, ch *EPGChannel) error {
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("parse xmltv channel: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "display-name":
				var s string
				if err := dec.DecodeElement(&s, &t); err != nil {
					return err
				}
				s = strings.TrimSpace(s)
				if s != "" {
					ch.Aliases = append(ch.Aliases, s)
					if ch.Name == "" {
						ch.Name = s
					}
				}
			case "icon":
				for _, a := range t.Attr {
					if a.Name.Local == "src" && ch.Icon == "" {
						ch.Icon = a.Value
					}
				}
				depth++
			default:
				depth++
			}
		case xml.EndElement:
			depth--
		}
	}
	return nil
}

// decodeProgramme дочитывает содержимое <programme> (title/desc/category);
// счётчик глубины для DecodeElement не трогаем — см. decodeChannel.
func decodeProgramme(dec *xml.Decoder, p *Programme) error {
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("parse xmltv programme: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "title":
				if p.Title == "" {
					var s string
					if err := dec.DecodeElement(&s, &t); err != nil {
						return err
					}
					p.Title = strings.TrimSpace(s)
				} else {
					depth++
				}
			case "desc":
				if p.Desc == "" {
					var s string
					if err := dec.DecodeElement(&s, &t); err != nil {
						return err
					}
					p.Desc = strings.TrimSpace(s)
				} else {
					depth++
				}
			case "category":
				if p.Category == "" {
					var s string
					if err := dec.DecodeElement(&s, &t); err != nil {
						return err
					}
					p.Category = strings.TrimSpace(s)
				} else {
					depth++
				}
			default:
				depth++
			}
		case xml.EndElement:
			depth--
		}
	}
	return nil
}

// charsetReader — минимальная поддержка не-UTF8 кодировок в XMLTV
// (windows-1251 встречается у части генераторов).
func charsetReader(charset string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "", "utf-8", "utf8", "us-ascii", "ascii":
		return input, nil
	}
	return nil, fmt.Errorf("xmltv: неподдерживаемая кодировка %q", charset)
}
