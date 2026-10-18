package releasewatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
)

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (w *Worker) Run(ctx context.Context) {
	if w.Store == nil || w.Discovery == nil || w.Bot == nil {
		return
	}
	for ctx.Err() == nil {
		release, acquired, err := w.Store.AcquireNotificationLeader(ctx)
		if err != nil {
			log.Printf("notifications: leader unavailable")
		}
		if acquired {
			w.runLeader(ctx)
			release()
		}
		if !wait(ctx, 30*time.Second) {
			return
		}
	}
}

func (w *Worker) runLeader(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var wg sync.WaitGroup
	for _, run := range []func(context.Context){w.poll, w.schedule, w.deliverLoop} {
		wg.Add(1)
		go func(fn func(context.Context)) { defer wg.Done(); fn(ctx) }(run)
	}
	for wait(ctx, 10*time.Second) {
		alive, err := w.Store.NotificationLeaderAlive(ctx)
		if err != nil || !alive {
			log.Printf("notifications: leader connection lost")
			cancel()
			break
		}
	}
	wg.Wait()
}

func (w *Worker) schedule(ctx context.Context) {
	interval := w.ScanInterval
	if interval <= 0 {
		interval = CheckInterval
	}
	for ctx.Err() == nil {
		cursor, err := w.Store.NotificationCursor(ctx, "release_last_scan")
		if err != nil {
			if !wait(ctx, time.Minute) {
				return
			}
			continue
		}
		last, parseErr := time.Parse(time.RFC3339Nano, cursor)
		if cursor != "" && parseErr != nil {
			log.Printf("notifications: invalid scan cursor")
			if !wait(ctx, time.Minute) {
				return
			}
			continue
		}
		due := last.Add(interval).Sub(w.now())
		if cursor != "" && due > 0 {
			if !wait(ctx, min(due, time.Minute)) {
				return
			}
			continue
		}
		started := w.now()
		// The cycle cannot overlap itself or run forever on dead trackers.
		scanCtx, cancel := context.WithTimeout(ctx, 5*time.Hour)
		err = w.Scan(scanCtx)
		cancel()
		if err != nil {
			log.Printf("notifications: scan interrupted")
			if !wait(ctx, time.Minute) {
				return
			}
			continue
		}
		if err := w.Store.SaveNotificationCursor(ctx, "release_last_scan", started.Format(time.RFC3339Nano)); err != nil {
			log.Printf("notifications: failed to save scan schedule")
		}
		if !wait(ctx, time.Minute) {
			return
		}
	}
}

func (w *Worker) deliverLoop(ctx context.Context) {
	for ctx.Err() == nil {
		if err := w.Deliver(ctx); err != nil && ctx.Err() == nil {
			log.Printf("notifications: delivery queue unavailable")
		}
		if !wait(ctx, 10*time.Second) {
			return
		}
	}
}

func (w *Worker) Deliver(ctx context.Context) error {
	for range 20 {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		blocked, err := w.deliveryBlocked(ctx)
		if err != nil {
			return err
		}
		if blocked {
			return nil
		}
		alive, err := w.Store.NotificationLeaderAlive(ctx)
		if err != nil {
			return err
		}
		if !alive {
			return errors.New("notification leadership lost")
		}
		// Re-read after every send: pause, unsubscribe or relinking must also
		// cancel later messages from this batch, not only the next batch.
		messages, err := w.Store.PendingNotifications(ctx, 1)
		if err != nil {
			return err
		}
		if len(messages) == 0 {
			return nil
		}
		message := messages[0]
		err = w.Bot.Send(ctx, message.ChatID, message.Text)
		if err == nil {
			if err := w.Store.MarkNotificationSent(ctx, message.ID); err != nil {
				return err
			}
		} else {
			var api *APIError
			if errors.As(err, &api) && api.Code == 403 {
				if err := w.Store.StopTelegramChat(ctx, message.ChatID); err != nil {
					return err
				}
				continue
			}
			delay := min(time.Hour, time.Minute*time.Duration(1<<min(max(0, message.Attempts), 6)))
			if api != nil && api.RetryAfter > delay {
				delay = api.RetryAfter
			}
			if retryErr := w.Store.RetryNotification(ctx, message.ID, delay); retryErr != nil {
				return retryErr
			}
			if api != nil && api.Code == 429 {
				if err := w.Store.SaveNotificationCursor(ctx, "telegram_not_before", w.now().Add(delay).Format(time.RFC3339Nano)); err != nil {
					return err
				}
				return nil
			}
		}
		// Stay below Telegram's per-chat limit without postponing the entire
		// release scan. Failed deliveries remain in the durable queue.
		if !wait(ctx, 1100*time.Millisecond) {
			return ctx.Err()
		}
	}
	return nil
}

func (w *Worker) deliveryBlocked(ctx context.Context) (bool, error) {
	value, err := w.Store.NotificationCursor(ctx, "telegram_not_before")
	if err != nil || value == "" {
		return false, err
	}
	until, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return true, errors.New("invalid telegram delivery cursor")
	}
	return w.now().Before(until), nil
}

func (w *Worker) reply(ctx context.Context, chatID int64, text string) {
	blocked, err := w.deliveryBlocked(ctx)
	if err != nil || blocked {
		return
	}
	err = w.Bot.Send(ctx, chatID, text)
	var api *APIError
	if errors.As(err, &api) && api.Code == 429 {
		_ = w.Store.SaveNotificationCursor(ctx, "telegram_not_before", w.now().Add(max(time.Minute, api.RetryAfter)).Format(time.RFC3339Nano))
	}
}

func (w *Worker) poll(ctx context.Context) {
	for ctx.Err() == nil {
		cursor, err := w.Store.NotificationCursor(ctx, "telegram_offset")
		if err != nil {
			if !wait(ctx, 5*time.Second) {
				return
			}
			continue
		}
		offset, err := strconv.ParseInt(cursor, 10, 64)
		if cursor != "" && err != nil {
			log.Printf("telegram: invalid update cursor")
			if !wait(ctx, 30*time.Second) {
				return
			}
			continue
		}
		alive, err := w.Store.NotificationLeaderAlive(ctx)
		if err != nil || !alive {
			if !wait(ctx, 5*time.Second) {
				return
			}
			continue
		}
		updates, err := w.Bot.Updates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("telegram: updates unavailable: %v", err)
			if !wait(ctx, 5*time.Second) {
				return
			}
			continue
		}
		for _, update := range updates {
			if update.ID < offset {
				continue
			}
			if err := w.handleUpdate(ctx, update); err != nil {
				log.Printf("telegram: command deferred")
				break
			}
			offset = update.ID + 1
			if err := w.Store.SaveNotificationCursor(ctx, "telegram_offset", strconv.FormatInt(offset, 10)); err != nil {
				break
			}
		}
		// Protect against a malformed proxy returning empty polls immediately.
		if len(updates) == 0 && !wait(ctx, time.Second) {
			return
		}
	}
}

func (w *Worker) handleUpdate(ctx context.Context, update Update) error {
	m := update.Message
	if m == nil || m.Chat.Type != "private" || m.Chat.ID <= 0 || m.From.ID != m.Chat.ID {
		return nil
	}
	fields := strings.Fields(m.Text)
	if len(fields) == 0 {
		return nil
	}
	command := strings.Split(strings.ToLower(fields[0]), "@")[0]
	if command == "/stop" {
		if err := w.Store.StopTelegramChat(ctx, m.Chat.ID); err != nil {
			return err
		}
		w.reply(ctx, m.Chat.ID, "Уведомления Кинотеки приостановлены. Включить их снова можно в медиатеке сайта.")
		return nil
	}
	if command != "/start" {
		return nil
	}
	text := "Для подключения уведомлений откройте медиатеку Кинотеки и нажмите «Подключить Telegram»."
	if len(fields) == 2 && strings.HasPrefix(fields[1], "link_") {
		token := strings.TrimPrefix(fields[1], "link_")
		if decoded, err := hex.DecodeString(token); err == nil && len(decoded) == 24 {
			hash := sha256.Sum256([]byte(token))
			linked, err := w.Store.ClaimTelegramLink(ctx, hex.EncodeToString(hash[:]), m.Chat.ID, m.From.Username)
			if err != nil {
				return err
			}
			if linked {
				text = "Telegram подключён к Кинотеке. Каждые 6 часов проверяем ваши подписки: фильмы, новые серии, улучшения качества и новые озвучки. /stop — приостановить уведомления."
			} else {
				text = "Ссылка устарела, уже использована или этот Telegram подключён к другому аккаунту. Создайте новую ссылку в медиатеке; если нужно, отключите прежнюю привязку."
			}
		}
	}
	// A reply outage must not replay a consumed binding token or /stop command.
	w.reply(ctx, m.Chat.ID, text)
	return nil
}
