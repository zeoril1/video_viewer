package catalogapi

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestSearchAllIncludesAnimationAndMatchesCounts(t *testing.T) {
	svc := newCatalogService(nil, nil, nil)
	for i := 0; i < 9; i++ {
		item := CatalogItem{ID: fmt.Sprint(i), TitleRU: "Атака титанов", Kind: "feature", Year: 2015}
		if i < 7 {
			item.Genres = []string{"Animation"}
		}
		svc.allCache = append(svc.allCache, catalogEntry{item: item})
	}
	svc.allCachedAt = time.Now()
	for _, section := range []string{"", "all"} {
		items, total := svc.SearchPage(context.Background(), "атака титанов", section, "", "year", "", true, 1, 30)
		if total != 9 || len(items) != 9 {
			t.Fatalf("section=%q: %d/%d, want 9/9", section, len(items), total)
		}
	}
	_, movies := svc.SearchPage(context.Background(), "атака титанов", "movie", "", "year", "", true, 1, 30)
	if movies != 2 {
		t.Fatalf("movie section includes animation: %d", movies)
	}
	kinds, _ := svc.Meta(context.Background(), "атака титанов", "", true)
	if kinds["movie"] != 2 || kinds["cartoon"] != 7 {
		t.Fatal(kinds)
	}
}

func TestMetaReleaseFilterHasIndependentCache(t *testing.T) {
	svc := newCatalogService(nil, nil, nil)
	svc.allCache = []catalogEntry{
		{item: CatalogItem{ID: "released", Title: "Film", Kind: "feature", Year: 2000}},
		{item: CatalogItem{ID: "future", Title: "Film", Kind: "feature", Year: time.Now().Year() + 5, ReleaseDate: time.Now().AddDate(5, 0, 0).Format("2006-01-02")}},
	}
	svc.allCachedAt = time.Now()
	all, _ := svc.Meta(context.Background(), "Film", "", false)
	released, _ := svc.Meta(context.Background(), "Film", "", true)
	if all["movie"] != 2 || released["movie"] != 1 {
		t.Fatalf("all=%v released=%v", all, released)
	}
	_, total := svc.SearchPage(context.Background(), "Film", "all", "", "year", "", true, 1, 30)
	if released["movie"] != total {
		t.Fatalf("count=%d total=%d", released["movie"], total)
	}
}
