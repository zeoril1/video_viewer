// sources_filter.go — отсев раздач-однофамильцев: поиск идёт по РУССКОМУ названию, и трекеры отдают чужие фильмы
// с тем же переводом (у «Реальных пацанов» находился сериал «Blue Mountain State») — с сидами они попадали
// в источники любого сезона, т.к. сезон 0 (сборник) универсален для фронтенда.
//
// Правило отсева (намеренно осторожное): сегмент через « / » считаем ЧУЖИМ названием, только если он не совпадает
// с нашим (в т.ч. через точки/дефисы и с учётом словоформ по основе слова), похож на название (2+ слова кириллицей)
// и не делит ни одного слова-основы (от 4 букв) с нашими названиями. Латиницу не трогаем: у зарубежных фильмов
// в раздачах бывают английские варианты, которых нет в наших данных, и отбрасывать из-за них раздачу нельзя.
// Служебные сегменты («S11E1-20 of 20», «Сезон 5. Часть 2», «Фильм о сериале») названиями не считаются.
package catalogapi

import (
	"log"
	"strings"
	"unicode"
)

// nameStopWords — служебные слова сегментов-описаний раздачи, а не названия фильма («Сезон 5. Часть 2»).
var nameStopWords = map[string]bool{
	"сезон": true, "сезоны": true, "сезонов": true, "сезоне": true, "сезона": true,
	"серия": true, "серии": true, "серий": true, "сериям": true, "сериях": true,
	"часть": true, "части": true, "частей": true, "частями": true,
	"фильм": true, "фильма": true, "фильме": true,
	"сериал": true, "сериала": true, "сериале": true,
	"саундтрек": true, "озвучка": true, "озвучкой": true, "перевод": true,
	"субтитры": true, "полный": true, "полная": true, "полное": true,
	"полностью": true, "сборник": true, "обновляемая": true, "надпись": true,
}

// stemLen — сколько первых букв слова считаем «основой» для сопоставления словоформ: «пацаны»/«пацанов» → «паца».
const stemLen = 4

// titleMatcher — проверка соответствия заголовка раздачи фильму.
type titleMatcher struct {
	norm    []string // названия целиком, в нижнем регистре
	compact []string // названия без разделителей («realnyepatsany»)
	stems   []string // основы слов названий (по 4 буквы)
}

// newTitleMatcher готовит сопоставитель по названиям фильма; пустой пропускает всё — фильтровать не по чему.
func newTitleMatcher(titles []string) *titleMatcher {
	m := &titleMatcher{}
	for _, t := range titles {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		m.norm = append(m.norm, t)
		m.compact = append(m.compact, compactName(t))
		for _, w := range letterWords(t, stemLen) {
			m.stems = appendUnique(m.stems, stem(w))
		}
	}
	return m
}

// filter выбрасывает раздачи, которые относятся к ДРУГОМУ фильму.
func (m *titleMatcher) filter(filmID string, items []sourceItem) []sourceItem {
	if m == nil || len(m.norm) == 0 || len(items) == 0 {
		return items
	}
	out := make([]sourceItem, 0, len(items))
	for _, it := range items {
		if m.matches(it.Title) {
			out = append(out, it)
			continue
		}
		log.Printf("sources: %s: отброшена чужая раздача %q", filmID, it.Title)
	}
	return out
}

// matches — относится ли заголовок раздачи к нашему фильму: наше название упомянуто, чужих названий нет.
func (m *titleMatcher) matches(title string) bool {
	if m == nil || len(m.norm) == 0 {
		return true // фильтровать не по чему
	}
	low := strings.ToLower(title)
	if !m.mentionsTitle(low) {
		return false // нашего названия нет вообще — это не наш фильм
	}
	for _, seg := range strings.Split(low, "/") {
		if m.foreignName(seg) {
			return false
		}
	}
	return true
}

// mentionsTitle — упомянуто ли в строке наше название (целиком, без разделителей или словом-основой).
func (m *titleMatcher) mentionsTitle(low string) bool {
	if containsAny(low, m.norm) || containsAny(compactName(low), m.compact) {
		return true
	}
	for _, w := range letterWords(low, stemLen) {
		if containsAny(stem(w), m.stems) {
			return true
		}
	}
	return false
}

// foreignName — похож ли сегмент заголовка на название ЧУЖОГО фильма.
func (m *titleMatcher) foreignName(seg string) bool {
	// «Whiplash (2014) BDRip» → «Whiplash»: название стоит до скобки.
	if i := strings.IndexAny(seg, "(["); i >= 0 {
		seg = seg[:i]
	}
	seg = strings.TrimSpace(seg)
	if seg == "" || m.mentionsTitle(seg) {
		return false // это наше название (возможно, в другой словоформе)
	}
	words := letterWords(seg, 3)
	cyr := 0
	for _, w := range words {
		if isCyrillic(w) && !nameStopWords[w] {
			cyr++
		}
	}
	// Название — это два и более слова-имени кириллицей; служебные сегменты («Серии: E1-8 of 8») сюда не попадают.
	if cyr < 2 {
		return false
	}
	for _, w := range words {
		if containsAny(stem(w), m.stems) {
			return false // общая основа с нашим названием — это его вариант
		}
	}
	return true
}

// stem — основа слова: первые stemLen букв в нижнем регистре.
func stem(w string) string {
	rs := []rune(w)
	if len(rs) > stemLen {
		rs = rs[:stemLen]
	}
	return string(rs)
}

// compactName убирает из строки всё, кроме букв и цифр («Realnye patsany» → «realnyepatsany»), чтобы
// узнавать название, записанное в раздаче через точки/дефисы («Realnye.Patsany.2010»).
func compactName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// letterWords — слова строки (последовательности букв) длиной от minLength.
func letterWords(s string, minLength int) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) >= minLength {
			out = append(out, string(cur))
		}
		cur = cur[:0]
	}
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) {
			cur = append(cur, r)
			continue
		}
		flush()
	}
	flush()
	return out
}

// isCyrillic — слово записано кириллицей (смотрим по первой букве).
func isCyrillic(w string) bool {
	for _, r := range w {
		return unicode.Is(unicode.Cyrillic, r)
	}
	return false
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if sub != "" && strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}
