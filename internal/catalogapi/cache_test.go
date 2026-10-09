package catalogapi

import (
	"context"
	"testing"
	"time"
)

// testCatalogSvc заполняет кэш записями фильмов без подключения к БД.
func testCatalogSvc(t *testing.T, items []CatalogItem) *catalogService {
	t.Helper()
	svc := newCatalogService(nil, nil, nil)
	for _, item := range items {
		svc.allCache = append(svc.allCache, catalogEntry{item: item})
	}
	svc.allCachedAt = time.Now()
	return svc
}

func TestCatalogWithoutDatabase(t *testing.T) {
	svc := newCatalogService(nil, nil, nil)
	items := svc.All(context.Background())
	if items == nil || len(items) != 0 {
		t.Fatalf("All() = %v, want non-nil empty catalog", items)
	}
	if svc.allCache == nil {
		t.Fatal("empty catalog was not cached")
	}
	page, total := svc.SearchPage(context.Background(), "", "all", "", "", "", false, 1, 30)
	if len(page) != 0 || total != 0 {
		t.Fatalf("SearchPage() = %v, %d, want empty page", page, total)
	}
}

func TestCatalogAllCached(t *testing.T) {
	svc := testCatalogSvc(t, []CatalogItem{
		{ID: "m1", Title: "Первый"},
		{ID: "m2", Title: "Второй"},
	})
	a := svc.All(context.Background())
	b := svc.All(context.Background())
	if len(a) != 2 || len(b) != 2 {
		t.Fatalf("All() length = %d/%d, want 2", len(a), len(b))
	}
	// Повторный вызов в пределах TTL должен вернуть ТОТ ЖЕ слайс (кэш).
	if &a[0] != &b[0] {
		t.Error("All() не кэшируется: повторный вызов вернул другой слайс")
	}
}

func TestCatalogMetaCached(t *testing.T) {
	svc := testCatalogSvc(t, []CatalogItem{
		{ID: "m1", Title: "Боевик"},
		{ID: "m2", Title: "Комедия"},
	})
	k1, _ := svc.Meta(context.Background(), "", "")
	// Маркер: если повторный вызов вернёт ту же карту — это кэш, а не пересчёт.
	k1["__test__"] = 1
	k2, _ := svc.Meta(context.Background(), "", "")
	if k2["__test__"] != 1 {
		t.Error("Meta kinds не кэшируется: повторный вызов вернул другую карту")
	}
	delete(k1, "__test__")
	// Фильмы без типа -> обе записи в секции movie.
	if k1["movie"] != 2 {
		t.Errorf("kinds[movie] = %d, want 2", k1["movie"])
	}
	// Другой ключ кэша (активный жанр) — отдельная запись: ни одна запись
	// не имеет жанров -> kinds пустые.
	k3, _ := svc.Meta(context.Background(), "", "боевик")
	if k3["movie"] != 0 {
		t.Errorf("Meta(genre=боевик) kinds[movie] = %d, want 0 (другой кэш)", k3["movie"])
	}
}
