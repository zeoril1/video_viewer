// Пакет sync — фоновая джоба периодического обновления рейтингов
// фильмов каталога из TMDB (надёжный источник с мягким rate-limit
// ~40 req/s, в отличие от IMDb, который блокирует не-браузерные запросы).
// Джоба обновляет рейтинги ТОЛЬКО «популярных» фильмов (чарты IMDb+TMDB);
// рейтинг любого фильма также обновляется on-demand при открытии карточки
// (RefreshFilmRating).
package sync

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/imdb"
	"github.com/zeoril1/video_viewer/internal/tmdb"
)

// DefaultRatingsInterval — периодичность джобы обновления рейтингов.
const DefaultRatingsInterval = 6 * time.Hour

// RatingsRefresher периодически обновляет рейтинги (и даты выпуска)
// фильмов из TMDB. За один проход обрабатывает ограниченную партию самых
// «старых» по времени последнего обновления записей, с паузой между
// запросами — чтобы не упираться в лимиты API и не рисковать блокировкой.
type RatingsRefresher struct {
	db       *db.Repo
	tm       *tmdb.Client
	batch    int           // сколько фильмов за проход
	pace     time.Duration // пауза между запросами к TMDB
	interval time.Duration // как часто запускать проход
}

// NewRatingsRefresher создаёт джобу обновления рейтингов.
func NewRatingsRefresher(db *db.Repo, tm *tmdb.Client, batch int, pace, interval time.Duration) *RatingsRefresher {
	if batch <= 0 {
		batch = 200
	}
	if pace <= 0 {
		pace = 400 * time.Millisecond
	}
	if interval <= 0 {
		interval = DefaultRatingsInterval
	}
	return &RatingsRefresher{db: db, tm: tm, batch: batch, pace: pace, interval: interval}
}

// Run запускает обновление сразу при старте и далее каждые interval.
// Блокирующий; остановить можно отменой ctx.
func (r *RatingsRefresher) Run(ctx context.Context) {
	r.RefreshOnce(ctx)

	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.RefreshOnce(ctx)
		}
	}
}

// RefreshOnce выполняет один проход: обновляет рейтинги партии фильмов,
// которые дольше всего не обновлялись.
func (r *RatingsRefresher) RefreshOnce(ctx context.Context) {
	films, err := r.db.FilmsNeedingRatingRefresh(ctx, r.batch)
	if err != nil {
		log.Printf("ratings: list: %v", err)
		return
	}
	if len(films) == 0 {
		return
	}
	log.Printf("ratings: старт прохода (%d фильмов)", len(films))

	updated, skipped := 0, 0
	for i, f := range films {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Пауза между запросами, чтобы не долбить API.
		if i > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(r.pace):
			}
		}

		film, err := fetchFilmRating(ctx, r.tm, f)
		if err != nil {
			if errors.Is(err, errNoMatch) {
				// На TMDB нет совпадения — помечаем запись как «попробовано»
				// (rating_updated_at=now, рейтинг и tmdb_id не трогаем), чтобы
				// не переспрашивать её каждый проход, и ставим отметку
				// tmdb_not_found (отдельная таблица на админ-странице).
				if derr := r.db.UpdateRating(ctx, f.IMDBID, 0, 0, "", ""); derr != nil {
					log.Printf("ratings: backoff %s: %v", f.IMDBID, derr)
				}
				if derr := r.db.SetTMDBNotFound(ctx, f.IMDBID, true); derr != nil {
					log.Printf("ratings: mark not-found %s: %v", f.IMDBID, derr)
				}
				log.Printf("ratings: %s: совпадение на TMDB не найдено", f.IMDBID)
			} else {
				log.Printf("ratings: %s: %v", f.IMDBID, err)
			}
			skipped++
			continue
		}
		// Привязываем найденный tmdb_id — запись «подключается» к TMDB,
		// и в следующий раз рейтинг обновится точно по нему (ByID).
		if err := r.db.UpdateRating(ctx, f.IMDBID, film.Rating, film.Votes, film.ReleaseDate,
			strconv.FormatInt(film.TMDBID, 10)); err != nil {
			log.Printf("ratings: update %s: %v", f.IMDBID, err)
			skipped++
			continue
		}
		// Найден — снимаем отметку «не найден на TMDB» (если была).
		if derr := r.db.SetTMDBNotFound(ctx, f.IMDBID, false); derr != nil {
			log.Printf("ratings: unmark %s: %v", f.IMDBID, derr)
		}
		updated++
	}
	log.Printf("ratings: проход завершён: обновлено %d, пропущено %d", updated, skipped)
}

// errNoMatch — на TMDB не удалось найти тот же фильм (нет совпадения).
var errNoMatch = errors.New("no matching film on TMDB")

// RefreshFilmData заполняет недостающие данные одного фильма из TMDB и,
// если передан imdb-клиент, добирает рейтинг/голоса IMDb и русские
// название/описание (on-demand при открытии карточки, кнопка «Обновить»
// в админке и одноразовый бэкфилл cmd/backfill): рейтинг, голоса, дату
// выпуска, русское и английское описания, жанры, постер, длительность,
// режиссёра/актёров и привязывает tmdb_id. Уже заполненные поля не
// затираются. Не блокирует HTTP-запрос — вызывается в фоне.
func RefreshFilmData(ctx context.Context, repo *db.Repo, tm *tmdb.Client, im *imdb.Client, id string) error {
	f, ok, err := repo.GetByIMDBID(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("film %s not found", id)
	}
	film, err := fetchFilmRating(ctx, tm, f)
	if err != nil {
		// «Нет совпадения» — помечаем запись (отдельная таблица админки;
		// из списка «пустых полей» такие исключаются). Сетевые ошибки не
		// помечаем — это не «не найден», а временный сбой.
		if errors.Is(err, errNoMatch) {
			if derr := repo.SetTMDBNotFound(ctx, id, true); derr != nil {
				log.Printf("refresh %s: mark not-found: %v", id, derr)
			}
		}
		return err
	}

	d := db.Film{
		Title:       film.Title,
		TitleRU:     film.TitleRU,
		Kind:        film.Kind,
		PlotRU:      film.OverviewRU,
		Genres:      film.Genres,
		PosterURL:   film.PosterURL,
		TMDBID:      strconv.FormatInt(film.TMDBID, 10),
		RatingTMDB:  film.Rating,
		VotesTMDB:   film.Votes,
		ReleaseDate: film.ReleaseDate,
		MovieLength: film.MovieLength,
	}
	// Английское описание — отдельным запросом en-US, если его ещё нет.
	if f.Plot == "" {
		d.Plot = tm.Overview(ctx, film.TMDBID, film.Kind, "en-US")
	}
	// Режиссёр/актёры из credits, если их ещё нет.
	if f.Director == "" || len(f.Actors) == 0 {
		dir, actors, _ := tm.Credits(ctx, film.TMDBID, film.Kind)
		d.Director, d.Actors = dir, actors
	}
	// Добираем из IMDb то, чего не дал TMDB: рейтинг/голоса IMDb (JSON-LD,
	// если IMDb доступен) и русские название/описание (Wikidata → Википедия).
	if im != nil && (d.Rating <= 0 || d.PlotRU == "" || d.TitleRU == "") {
		if imf, err := im.GetByID(ctx, id); err == nil {
			if d.Rating <= 0 && imf.Rating > 0 {
				d.Rating = imf.Rating
				d.Votes = int64(imf.Votes)
			}
			if d.PlotRU == "" && imf.PlotRU != "" {
				d.PlotRU = imf.PlotRU
			}
			if d.TitleRU == "" && imf.TitleRU != "" {
				d.TitleRU = imf.TitleRU
			}
		}
	}
	return repo.UpdateFilmData(ctx, f.IMDBID, d)
}

// fetchFilmRating получает свежий рейтинг фильма из TMDB. Приоритет:
//  1. по сохранённому tmdb_id (точно);
//  2. по настоящему IMDb-адресу (tt...), если он есть;
//  3. поиском по названию с проверкой совпадения (год, название RU/EN,
//     при наличии — режиссёр/актёры) — для записей без IMDb-ссылки,
//     например синтетических id Кинопоиска ("kp...").
//
// Искать по самому id "kp..." нельзя — это не IMDb-ссылка, TMDB его не
// знает, и он мог бы совпасть с чужим фильмом.
func fetchFilmRating(ctx context.Context, tm *tmdb.Client, f db.Film) (tmdb.Film, error) {
	if f.TMDBID != "" {
		if id, err := strconv.ParseInt(f.TMDBID, 10, 64); err == nil && id > 0 {
			// По типу: сериалы — /tv/{id}, иначе /movie/{id}, иначе /movie
			// мог бы вернуть другой фильм с тем же числовым id.
			if film, err := tm.ByIDKind(ctx, id, f.Kind); err == nil {
				return film, nil
			}
		}
	}
	if strings.HasPrefix(f.IMDBID, "tt") {
		if film, err := tm.FindByIMDB(ctx, f.IMDBID); err == nil {
			return film, nil
		}
	}
	return searchAndMatch(ctx, tm, f)
}

// searchAndMatch ищет фильм на TMDB по названию и убеждается, что это
// тот же фильм: сравнивает год и название (в т.ч. на другом языке), а
// если у записи есть режиссёр/актёры — и их (через /credits).
func searchAndMatch(ctx context.Context, tm *tmdb.Client, f db.Film) (tmdb.Film, error) {
	q := f.TitleRU
	if q == "" {
		q = f.Title
	}
	if strings.TrimSpace(q) == "" {
		return tmdb.Film{}, errNoMatch
	}

	cands, err := tm.SearchLite(ctx, strings.TrimSpace(q), 10)
	if err != nil {
		return tmdb.Film{}, err
	}

	best := tmdb.Film{}
	bestScore := 0
	for _, c := range cands {
		if s := filmMatchScore(f, c); s > bestScore {
			best, bestScore = c, s
		}
	}
	if bestScore == 0 {
		return tmdb.Film{}, errNoMatch
	}

	// Сверка по режиссёру/актёрам, если у записи они есть.
	if f.Director != "" || len(f.Actors) > 0 {
		dir, actors, err := tm.Credits(ctx, best.TMDBID, best.Kind)
		if err == nil && !creditsAgree(f, dir, actors) {
			return tmdb.Film{}, errNoMatch
		}
	}
	return best, nil
}

// filmMatchScore — насколько кандидат TMDB соответствует фильму БД
// (0 — не соответствует). Требуются совпадение названия (RU или EN:
// точное нормализованное либо вложенное) и года. Год допускает разницу
// в ±1 — у разных источников дата премьеры может отличаться на год
// (например, «Обсессия» 2025 у КП, но 2026 на TMDB).
func filmMatchScore(f db.Film, c tmdb.Film) int {
	yearScore := 0
	if f.Year > 0 {
		diff := c.Year - f.Year
		if diff < 0 {
			diff = -diff
		}
		switch {
		case diff == 0:
			yearScore = 40
		case diff == 1:
			yearScore = 20
		default:
			return 0
		}
	}

	titleScore := 0
	switch {
	case titleExact(f.TitleRU, c.TitleRU) || titleExact(f.TitleRU, c.Title) ||
		titleExact(f.Title, c.Title) || titleExact(f.Title, c.TitleRU):
		titleScore = 60
	case titleMatches(f.TitleRU, c.TitleRU) || titleMatches(f.TitleRU, c.Title) ||
		titleMatches(f.Title, c.Title) || titleMatches(f.Title, c.TitleRU):
		titleScore = 30
	}
	if titleScore == 0 {
		return 0
	}

	score := titleScore + yearScore
	// Предпочтение точному совпадению русского названия записи.
	if titleExact(f.TitleRU, c.TitleRU) || titleExact(f.TitleRU, c.Title) {
		score += 5
	}
	return score
}

// titleMatches — названия совпадают: точное нормализованное равенство
// либо одно содержит другое (при достаточной длине, чтобы не ловить
// ложные совпадения на коротких словах).
func titleMatches(a, b string) bool {
	a, b = normTitle(a), normTitle(b)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	if len(a) >= 4 && (strings.Contains(a, b) || strings.Contains(b, a)) {
		return true
	}
	return false
}

// titleExact — точное (нормализованное) равенство названий.
func titleExact(a, b string) bool {
	a, b = normTitle(a), normTitle(b)
	return a != "" && a == b
}

// creditsAgree — совпадают ли режиссёр/актёры фильма БД с данными TMDB.
// Совпадение любого имени даёт согласие. Если совпадений нет, отклоняем
// только когда есть с чем сравнивать в ОДНОМ алфавите: имена в разных
// алфавитах (русская транслитерация режиссёра из Кинопоиска против
// английского имени в TMDB) сравнить нельзя — не спорим. Пустые данные
// с любой стороны тоже не являются поводом для отказа.
func creditsAgree(f db.Film, dir string, actors []string) bool {
	if f.Director != "" && dir != "" && normTitle(f.Director) == normTitle(dir) {
		return true
	}
	if len(f.Actors) > 0 && len(actors) > 0 {
		for _, a := range f.Actors {
			na := normTitle(a)
			if na == "" {
				continue
			}
			for _, b := range actors {
				if na == normTitle(b) {
					return true
				}
			}
		}
	}
	// Ничего не совпало. Отклоняем только при сравнимых (одноалфавитных)
	// данных; если данных нет или они в разных алфавитах — не спорим.
	if f.Director != "" && dir != "" && sameScript(f.Director, dir) {
		return false
	}
	if len(f.Actors) > 0 && len(actors) > 0 {
		for _, a := range f.Actors {
			for _, b := range actors {
				if sameScript(a, b) {
					return false
				}
			}
		}
	}
	return true
}

// sameScript — строки в одном алфавите (обе кириллические или обе
// латинские). Нужно, чтобы не сравнивать русские транслитерации имён
// с английскими напрямую.
func sameScript(a, b string) bool {
	return isCyrillic(a) == isCyrillic(b)
}

// isCyrillic — содержит ли строка кириллические символы.
func isCyrillic(s string) bool {
	for _, r := range s {
		if r >= '\u0400' && r <= '\u04FF' {
			return true
		}
	}
	return false
}

// normTitle нормализует название для сравнения: нижний регистр, только
// буквы и цифры, пробелы схлопываются в один.
func normTitle(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
		} else if !space && b.Len() > 0 {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}
