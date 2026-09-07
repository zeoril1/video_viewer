package catalogapi

import (
	"context"
	"testing"

	"github.com/zeoril1/video_viewer/internal/catalog"
)

// testCatalogSvc строит сервис каталога только из магнет-записей (без БД)
// — для проверки кэширования All/Meta.
func testCatalogSvc(t *testing.T, items []catalog.Item) *catalogService {
	t.Helper()
	return newCatalogService(&catalog.Catalog{Items: items}, nil, nil, nil)
}

func TestCatalogAllCached(t *testing.T) {
	svc := testCatalogSvc(t, []catalog.Item{
		{ID: "m1", Title: "Первый", Magnet: "magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{ID: "m2", Title: "Второй", Magnet: "magnet:?xt=urn:btih:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
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
	if a[0].item.Source != "magnet" || !a[0].item.HasMagnet {
		t.Errorf("All()[0] = %+v, want magnet-запись", a[0].item)
	}
}

func TestCatalogMetaCached(t *testing.T) {
	svc := testCatalogSvc(t, []catalog.Item{
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
	// Магнеты без типа -> обе записи в секции movie.
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
