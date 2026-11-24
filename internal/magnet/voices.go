package magnet

import (
	"html"
	"regexp"
	"strings"

	xhtml "golang.org/x/net/html"
)

// These names describe audio metadata, never words in a film synopsis or a
// related-release title. Keep canonical labels compatible with the player.
var voiceNames = []struct {
	name string
	re   *regexp.Regexp
}{
	{"MovieDalen", voicePattern(`movie[ ._-]?dalen`)},
	{"HDRezka Studio", voicePattern(`hdrezka\s*studio`)},
	{"HDRezka", voicePattern(`hdrezka|(?-i:HDr)`)},
	{"LostFilm", voicePattern(`lostfilm|lf`)},
	{"NewStudio", voicePattern(`new\s*studio`)},
	{"TVShows", voicePattern(`tvshows|tvs`)},
	{"ViruseProject", voicePattern(`viruse\s*project`)},
	{"Смотри кино", voicePattern(`смотри\s+кино`)},
	{"Dragon Money Studio", voicePattern(`dragon\s+money\s+studio`)},
	{"BaibaKo", voicePattern(`baiba\s*ko`)},
	{"Jaskier", voicePattern(`jaskier`)},
	{"Gears Media", voicePattern(`gears\s*media|gears`)},
	{"Amedia", voicePattern(`amedia`)},
	{"НоваМедиа", voicePattern(`novamedia|нова\s*медиа`)},
	{"Невафильм", voicePattern(`невафильм`)},
	{"ColdFilm", voicePattern(`coldfilm`)},
	{"NewComers", voicePattern(`newcomers`)},
	{"LE-Production", voicePattern(`le[- ]?production`)},
	{"Red Head Sound", voicePattern(`red\s+head(?:\s+sound)?|rhs`)},
	{"Продубляж", voicePattern(`продубляж`)},
	{"Сыендук", voicePattern(`сыендук|съендук|syenduk`)},
	{"Кураж-Бамбей", voicePattern(`кураж[- ]бамбей|кураж`)},
	{"Кипарис", voicePattern(`кипарис`)},
	{"Кириллица", voicePattern(`кириллица|кирилица`)},
	{"RG.Paravozik", voicePattern(`rg\.?\s*paravozik|paravozik|паравозик|паравоз`)},
	{"Котов", voicePattern(`котов`)},
	{"Яроцкий", voicePattern(`яроцкий|яроцкого|yarotsky`)},
	{"Сербин", voicePattern(`сербин|serbin`)},
	{"Доценко", voicePattern(`доценко|dotsenko`)},
	{"Гоблин", voicePattern(`гоблин|goblin|пучков`)},
	{"Кубик в кубе", voicePattern(`кубик\s+в\s+кубе`)},
}

func voicePattern(aliases string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)(?:^|[^\pL\pN])(?:` + aliases + `)(?:$|[^\pL\pN])`)
}

var (
	audioMetadataLine = regexp.MustCompile(`(?i)^(?:перевод|озвучка|озвучивание|дубляж|аудио(?:дорожк[аи])?|звуковая\s+дорожка|translation|audio(?:\s*(?:track|stream))?)\s*(?:[№#]\s*)?\d*\s*[:|.\-]`)
	mediaInfoAudio    = regexp.MustCompile(`(?i)^(?:audio|аудио)(?:\s*(?:[№#]\s*)?\d+)?\s*$`)
	mediaInfoOther    = regexp.MustCompile(`(?i)^(?:video|видео|text|текст|menu|меню|general|общее)(?:\s*#?\s*\d+)?\s*$`)
	mediaInfoTitle    = regexp.MustCompile(`(?i)^(?:title|заголовок|название)\s*:`)
)

func mergeVoices(groups ...[]string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, voices := range groups {
		for _, voice := range voices {
			if voice != "" && !seen[voice] {
				seen[voice] = true
				out = append(out, voice)
			}
		}
	}
	if seen["HDRezka Studio"] && seen["HDRezka"] {
		for i, v := range out {
			if v == "HDRezka" {
				out = append(out[:i], out[i+1:]...)
				break
			}
		}
	}
	return out
}

func jackettXMLText(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "<![CDATA[")
	s = strings.TrimSuffix(s, "]]>")
	return strings.TrimSpace(html.UnescapeString(s))
}

func voicesFromDescription(description string) []string {
	if !strings.Contains(description, "<") {
		return voicesFromMetadataText(html.UnescapeString(description))
	}
	doc, err := xhtml.Parse(strings.NewReader(description))
	if err != nil {
		return nil
	}
	return voicesFromMetadataText(metadataText(doc))
}

func voicesFromMetadataText(text string) []string {
	var voices []string
	inAudio := false
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if mediaInfoAudio.MatchString(line) {
			inAudio = true
			continue
		}
		if mediaInfoOther.MatchString(line) {
			inAudio = false
		}
		if !audioMetadataLine.MatchString(line) && !(inAudio && mediaInfoTitle.MatchString(line)) {
			continue
		}
		for _, voice := range voiceNames {
			if voice.re.MatchString(line) {
				voices = mergeVoices(voices, []string{voice.name})
			}
		}
	}
	return voices
}

// Preserve metadata line boundaries, but skip scripts, quoted HTML and related
// release lists. A recommendation inside #details is not an audio track.
func metadataText(root *xhtml.Node) string {
	var b strings.Builder
	var walk func(*xhtml.Node, bool)
	walk = func(n *xhtml.Node, pre bool) {
		if n.Type == xhtml.ElementNode {
			switch n.Data {
			case "script", "style", "textarea", "nav", "blockquote":
				return
			}
			for _, attr := range n.Attr {
				v := strings.ToLower(attr.Val)
				if attr.Key == "id" && (v == "index" || v == "comments" || v == "displayfiles") {
					return
				}
				if attr.Key == "class" && (strings.Contains(v, "related") || strings.Contains(v, "recommend")) {
					return
				}
			}
			if n.Data == "br" || isMetadataBlock(n.Data) {
				b.WriteByte('\n')
			}
			pre = pre || n.Data == "pre"
		}
		if n.Type == xhtml.TextNode {
			if pre {
				b.WriteString(n.Data)
			} else {
				b.WriteString(strings.Join(strings.Fields(n.Data), " "))
				b.WriteByte(' ')
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, pre)
		}
		if n.Type == xhtml.ElementNode && isMetadataBlock(n.Data) {
			b.WriteByte('\n')
		}
	}
	walk(root, false)
	return b.String()
}

func isMetadataBlock(tag string) bool {
	switch tag {
	case "p", "div", "tr", "li", "pre", "hr":
		return true
	}
	return false
}
