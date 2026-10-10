package catalogapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestCatalogPaginationHTTP(t *testing.T) {
	server := NewServer(Config{})
	maxInt := strconv.Itoa(int(^uint(0) >> 1))
	cases := []struct {
		name, query           string
		status, page, perPage int
	}{
		{"defaults", "", 200, 1, 30},
		{"client page size", "page=2&per_page=30", 200, 2, 30},
		{"maximum page size", "per_page=100", 200, 1, 100},
		{"maximum page is a valid empty result", "page=" + maxInt, 200, int(^uint(0) >> 1), 30},
		{"empty page", "page=", 400, 0, 0},
		{"empty page size", "per_page=", 400, 0, 0},
		{"zero page", "page=0", 400, 0, 0},
		{"negative page", "page=-1", 400, 0, 0},
		{"not a page", "page=abc", 400, 0, 0},
		{"fractional page", "page=1.5", 400, 0, 0},
		{"page overflow", "page=99999999999999999999999999999", 400, 0, 0},
		{"repeated page", "page=1&page=2", 400, 0, 0},
		{"zero page size", "per_page=0", 400, 0, 0},
		{"negative page size", "per_page=-1", 400, 0, 0},
		{"not a page size", "per_page=no", 400, 0, 0},
		{"too large page size", "per_page=101", 400, 0, 0},
		{"page size overflow", "per_page=99999999999999999999999999", 400, 0, 0},
		{"repeated page size", "per_page=10&per_page=30", 400, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			server.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/catalog?"+tc.query, nil))
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d; body = %s", w.Code, tc.status, w.Body)
			}
			if !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("not JSON: %s", w.Header().Get("Content-Type"))
			}
			if tc.status == 400 {
				var result struct{ Error, Message string }
				if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if result.Error != "invalid_pagination" || result.Message == "" {
					t.Fatalf("invalid error response: %+v", result)
				}
				return
			}
			var result struct {
				Items      []CatalogItem `json:"items"`
				Page       int           `json:"page"`
				PerPage    int           `json:"per_page"`
				Total      int           `json:"total"`
				TotalPages int           `json:"total_pages"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Page != tc.page || result.PerPage != tc.perPage || result.Items == nil || len(result.Items) != 0 || result.Total != 0 || result.TotalPages != 0 {
				t.Fatalf("unexpected empty catalog response: %+v", result)
			}
		})
	}
}

func TestSearchPageBoundaries(t *testing.T) {
	items := make([]CatalogItem, 61)
	for i := range items {
		items[i] = CatalogItem{ID: fmt.Sprintf("m%03d", i), Title: fmt.Sprintf("Film %03d", i)}
	}
	svc := testCatalogSvc(t, items)
	maxInt := int(^uint(0) >> 1)
	cases := []struct {
		name                        string
		page, perPage, first, count int
	}{
		{"first", 1, 30, 0, 30},
		{"middle", 2, 30, 30, 30},
		{"last", 3, 30, 60, 1},
		{"beyond", 4, 30, 0, 0},
		{"maximum int page", maxInt, 30, 0, 0},
		{"maximum page and size", maxInt, maxInt, 0, 0},
		{"defaults for direct caller", 0, 0, 0, 30},
		{"negative direct caller", -1, -1, 0, 30},
		{"oversized direct page size", 1, maxInt, 0, 61},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page, total := svc.SearchPage(context.Background(), "", "all", "", "title", "", false, tc.page, tc.perPage)
			if total != len(items) || page == nil || len(page) != tc.count {
				t.Fatalf("page length = %d, total = %d; want %d/%d", len(page), total, tc.count, len(items))
			}
			for i, entry := range page {
				if want := items[tc.first+i].ID; entry.item.ID != want {
					t.Fatalf("item %d = %s, want %s", i, entry.item.ID, want)
				}
			}
		})
	}
	page, total := svc.SearchPage(context.Background(), "no matches", "all", "", "", "", false, maxInt, maxInt)
	if page == nil || len(page) != 0 || total != 0 {
		t.Fatalf("empty filtered catalog = %v/%d", page, total)
	}
}
