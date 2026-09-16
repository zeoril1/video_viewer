package streamapi

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestLatestHLSLaunchWins(t *testing.T) {
	m := newHLSManager("http://localhost")
	key := hlsSessionKey("tt123", "viewer")
	older := m.beginLaunchLocked(key)
	newer := m.beginLaunchLocked(key)
	latest := &hlsSession{dir: t.TempDir()}
	if !m.publishSession(context.Background(), key, newer, latest) {
		t.Fatal("latest launch rejected")
	}
	m.finishLaunch(key, newer)
	if m.publishSession(context.Background(), key, older, &hlsSession{}) {
		t.Fatal("older launch replaced newer session")
	}
	if m.sessions[key] != latest {
		t.Fatal("latest session lost")
	}
	pending := m.beginLaunchLocked(key)
	m.stopSessions(key)
	if m.publishSession(context.Background(), key, pending, &hlsSession{}) {
		t.Fatal("stopped launch resurrected session")
	}
	pending = m.beginLaunchLocked(key)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if m.publishSession(ctx, key, pending, &hlsSession{}) {
		t.Fatal("canceled request published session")
	}
	m.finishLaunch(key, pending)
}

func TestFailedHLSProcessIsNotReused(t *testing.T) {
	m := newHLSManager("http://localhost")
	// Заставляем новую попытку упасть до ffmpeg: ensure должен перезапустить,
	// а не вернуть мёртвую сессию.
	m.dataDir = filepath.Join(t.TempDir(), "missing")
	key := hlsSessionKey("tt123", "viewer")
	old := &hlsSession{magnet: "m", start: 10, quality: "source", dir: t.TempDir(), exited: true, exitErr: errors.New("ffmpeg failed")}
	m.sessions[key] = old
	got, err := m.ensure(context.Background(), "tt123", "m", 0, 0, 0, 10, "source", "viewer")
	if err == nil || got != nil || m.sessions[key] != nil {
		t.Fatal("failed process reused")
	}
	if len(m.launches) != 0 {
		t.Fatal("launch leaked on error")
	}
	// Успешное завершение — валидный кеш VOD, а не сбой ffmpeg.
	old.exitErr = nil
	m.sessions[key] = old
	got, err = m.ensure(context.Background(), "tt123", "m", 0, 0, 0, 10, "source", "viewer")
	if err != nil || got != old {
		t.Fatal("completed VOD was not reused")
	}
}
