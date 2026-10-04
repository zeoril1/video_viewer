package authapi

import (
	"crypto/subtle"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Rooms are intentionally ephemeral: restarting auth closes all invitations.
type roomPlayback struct {
	ID       string  `json:"id"`
	Magnet   string  `json:"magnet"`
	File     int     `json:"file"`
	Season   int     `json:"season"`
	Episode  int     `json:"episode"`
	Release  string  `json:"release"`
	Position float64 `json:"position"`
	Paused   bool    `json:"paused"`
}
type watchRoom struct {
	State    roomPlayback `json:"state"`
	Updated  int64        `json:"updated"`
	Revision uint64       `json:"revision"`
	owner    string
	secret   string
	expires  time.Time
	members  map[string]roomMember
}
type roomMember struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	seen time.Time
}

const roomPresenceTTL = 15 * time.Second

var roomMemberID = regexp.MustCompile(`^[a-f0-9]{32}$`)

type roomStore struct {
	mu    sync.Mutex
	rooms map[string]*watchRoom
}

func newRoomStore() *roomStore { return &roomStore{rooms: make(map[string]*watchRoom)} }
func validRoomPlayback(s roomPlayback) bool {
	return len(s.ID) > 0 && len(s.ID) <= 128 && strings.HasPrefix(s.Magnet, "magnet:?") && len(s.Magnet) <= 8192 && len(s.Release) <= 1024 && s.File >= -1 && s.File < 100000 && s.Season >= 0 && s.Episode >= 0 && !math.IsNaN(s.Position) && !math.IsInf(s.Position, 0) && s.Position >= 0 && s.Position <= 7*24*3600
}
func (s *roomStore) handler(auth *authHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		u, _ := auth.currentUser(r)
		if r.Method != "GET" && !checkOrigin(w, r) {
			return
		}
		s.serve(w, r, u.ID, u.Username)
	}
}
func (s *roomStore) serve(w http.ResponseWriter, r *http.Request, uid int64, usernames ...string) {
	ip := strings.TrimSuffix(clientKey(r, ""), "|")
	owner := "ip:" + ip
	member := roomMember{Name: ip, Kind: "guest"}
	if uid > 0 {
		owner = fmt.Sprintf("user:%d", uid)
		if len(usernames) > 0 {
			member.Name = usernames[0]
		} else {
			member.Name = owner
		}
		member.Kind = "user"
	}
	memberKey := r.Header.Get("X-Room-Member")
	if !roomMemberID.MatchString(memberKey) {
		memberKey = owner
	}
	leaving := strings.HasSuffix(r.URL.Path, "/leave")
	var state roomPlayback
	if (r.Method == "POST" && !leaving) || r.Method == "PUT" {
		if !readSmallJSON(w, r, &state) {
			return
		}
		if !validRoomPlayback(state) {
			http.Error(w, "Некорректное состояние просмотра.", 400)
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for id, room := range s.rooms {
		if now.After(room.expires) {
			delete(s.rooms, id)
		}
	}
	id := r.PathValue("room")
	if r.Method == "POST" && !leaving {
		owned := 0
		for _, room := range s.rooms {
			if room.owner == owner {
				owned++
			}
		}
		if owned >= 3 || len(s.rooms) >= 128 {
			http.Error(w, "Достигнут лимит комнат. Закройте предыдущую комнату.", 429)
			return
		}
		id = randomHex(16)
		room := &watchRoom{State: state, Updated: now.UnixMilli(), Revision: 1, owner: owner, secret: randomHex(24), expires: now.Add(30 * time.Minute), members: make(map[string]roomMember)}
		s.rooms[id] = room
		writePersonalJSON(w, map[string]any{"room": id, "host_token": room.secret})
		return
	}
	room := s.rooms[id]
	if room == nil {
		http.Error(w, "Комната закрыта или срок ссылки истёк.", 404)
		return
	}
	for key, participant := range room.members {
		if now.Sub(participant.seen) > roomPresenceTTL {
			delete(room.members, key)
		}
	}
	if leaving {
		delete(room.members, memberKey)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	isHost := subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Room-Host")), []byte(room.secret)) == 1
	if r.Method == "PUT" || r.Method == "DELETE" {
		if !isHost {
			http.Error(w, "Управлять просмотром может только ведущий.", 403)
			return
		}
		if r.Method == "DELETE" {
			delete(s.rooms, id)
			w.WriteHeader(204)
			return
		}
		room.State = state
		room.Updated = now.UnixMilli()
		room.Revision++
		room.expires = now.Add(30 * time.Minute)
	}
	response := map[string]any{"state": room.State, "updated": room.Updated, "revision": room.Revision, "server_time": now.UnixMilli()}
	if isHost {
		delete(room.members, memberKey)
		participants := make([]roomMember, 0, len(room.members))
		for _, participant := range room.members {
			participants = append(participants, participant)
		}
		sort.Slice(participants, func(i, j int) bool { return participants[i].Name < participants[j].Name })
		response["participants"] = participants
	} else {
		if _, exists := room.members[memberKey]; !exists && len(room.members) >= 128 {
			http.Error(w, "Достигнут лимит участников комнаты.", http.StatusTooManyRequests)
			return
		}
		member.seen = now
		room.members[memberKey] = member
	}
	writePersonalJSON(w, response)
}
