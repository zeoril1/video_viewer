package authapi

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRoomOwnershipExpiryAndLimits(t *testing.T) {
	s := newRoomStore()
	payload := `{"id":"tt1","magnet":"magnet:?xt=urn:btih:123","file":0,"position":12,"paused":false}`
	call := func(method, id, token, body string, user int64) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/rooms/"+id, strings.NewReader(body))
		r.SetPathValue("room", id)
		r.Header.Set("X-Room-Host", token)
		w := httptest.NewRecorder()
		s.serve(w, r, user)
		return w
	}
	created := call("POST", "", "", payload, 1)
	var link map[string]string
	_ = json.Unmarshal(created.Body.Bytes(), &link)
	id, token := link["room"], link["host_token"]
	if len(id) != 32 || token == "" {
		t.Fatal(created.Body.String())
	}
	if w := call("GET", id, "", "", 2); w.Code != 200 || strings.Contains(w.Body.String(), token) {
		t.Fatal("guest cannot read state or secret leaked")
	}
	for _, actor := range []struct {
		user  int64
		token string
	}{{2, ""}, {1, "wrong"}} {
		if w := call("PUT", id, actor.token, payload, actor.user); w.Code != 403 {
			t.Fatalf("unauthorized update: %d", w.Code)
		}
	}
	if w := call("PUT", id, token, strings.Replace(payload, `"position":12`, `"position":42`, 1), 1); w.Code != 200 || s.rooms[id].State.Position != 42 {
		t.Fatal("host update failed")
	}
	if w := call("PUT", id, token, strings.Replace(payload, `"position":12`, `"position":-1`, 1), 1); w.Code != 400 {
		t.Fatal("invalid position accepted")
	}
	for i := 0; i < 2; i++ {
		call("POST", "", "", payload, 1)
	}
	if w := call("POST", "", "", payload, 1); w.Code != 429 {
		t.Fatal("owner limit not enforced")
	}
	s.rooms[id].expires = time.Now().Add(-time.Second)
	if w := call("GET", id, "", "", 2); w.Code != 404 {
		t.Fatal("expired room survived")
	}
}

func TestAnonymousRoomsAndParticipantPrivacy(t *testing.T) {
	h := NewServer(Config{})
	payload := `{"id":"tt1","magnet":"magnet:?xt=urn:btih:123","file":0,"position":12}`
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/rooms", strings.NewReader(payload)))
	if w.Code != 200 {
		t.Fatal("anonymous creation failed", w.Code, w.Body.String())
	}
	var created map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	id, secret := created["room"], created["host_token"]
	call := func(method, member, token, ip, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/rooms/"+id, strings.NewReader(body))
		r.Header.Set("X-Room-Member", member)
		r.Header.Set("X-Room-Host", token)
		r.Header.Set("X-Video-Viewer-Client-IP", ip)
		out := httptest.NewRecorder()
		h.ServeHTTP(out, r)
		return out
	}
	guest := call("GET", strings.Repeat("a", 32), "", "2001:db8::123", "")
	if guest.Code != 200 || strings.Contains(guest.Body.String(), "participants") {
		t.Fatal("guest access/privacy", guest.Code, guest.Body.String())
	}
	host := call("PUT", strings.Repeat("b", 32), secret, "192.0.2.1", payload)
	if host.Code != 200 || !strings.Contains(host.Body.String(), "2001:db8::123") {
		t.Fatal("host cannot see guest IP", host.Body.String())
	}
	if out := call("PUT", strings.Repeat("a", 32), "", "2001:db8::123", payload); out.Code != 403 {
		t.Fatal("guest controlled room")
	}
	if out := call("DELETE", strings.Repeat("a", 32), "", "2001:db8::123", ""); out.Code != 403 {
		t.Fatal("guest closed room")
	}
}

func TestParticipantIdentityExpiryAndLeave(t *testing.T) {
	s := newRoomStore()
	s.rooms["room"] = &watchRoom{secret: "host-secret", expires: time.Now().Add(time.Hour), members: make(map[string]roomMember)}
	call := func(method, suffix, key string, uid int64, name string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/rooms/room"+suffix, nil)
		r.SetPathValue("room", "room")
		r.Header.Set("X-Room-Member", key)
		r.Header.Set("X-Video-Viewer-Client-IP", "198.51.100.7")
		w := httptest.NewRecorder()
		s.serve(w, r, uid, name)
		return w
	}
	key := strings.Repeat("c", 32)
	if w := call("GET", "", key, 7, "Андрей"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	member := s.rooms["room"].members[key]
	if member.Name != "Андрей" || member.Kind != "user" {
		t.Fatal(member)
	}
	call("GET", "", key, 0, "")
	if len(s.rooms["room"].members) != 1 || s.rooms["room"].members[key].Name != "198.51.100.7" {
		t.Fatal("logout did not refresh identity")
	}
	call("POST", "/leave", key, 0, "")
	if len(s.rooms["room"].members) != 0 {
		t.Fatal("leave did not remove participant")
	}
	member.seen = time.Now().Add(-roomPresenceTTL - time.Second)
	s.rooms["room"].members[key] = member
	call("GET", "", strings.Repeat("d", 32), 0, "")
	if _, exists := s.rooms["room"].members[key]; exists {
		t.Fatal("stale participant remains")
	}
}
