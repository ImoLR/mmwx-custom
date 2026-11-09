package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// 「删除服务器」 revokes the server's FLVX share and records the server's name
// and hosts on it. Once the server is gone the share list keeps returning the
// share as revoked from that record, so FLVX cleans up its side, then drops
// the share and its counters: after FLVX has listed it as revoked and
// flvxRetiredShareKeep has passed, or after flvxRetiredShareMax regardless.
// Both happen inside the share list FLVX already polls; nothing new polls.
const (
	flvxRetiredShareKeep = 24 * time.Hour
	flvxRetiredShareMax  = 30 * 24 * time.Hour
)

type flvxDeletedServer struct {
	Name  string     `json:"name"`
	Hosts flvx.Hosts `json:"hosts"`
	// Unix milliseconds: revoked for deletion / first listed to FLVX after that.
	At     int64 `json:"at"`
	SeenAt int64 `json:"seen_at,omitempty"`
}

func flvxRetiredShareExpired(d *flvxDeletedServer, now time.Time) bool {
	age := now.Sub(time.UnixMilli(d.At))
	return age >= flvxRetiredShareMax || d.SeenAt > 0 && age >= flvxRetiredShareKeep
}

func flvxHostsEmpty(h flvx.Hosts) bool { return h.Domain == "" && h.IPv4 == "" && h.IPv6 == "" }

// revokeFLVXShareForDeletion is the 「撤销 FLVX 共享」 step of 「删除服务器」:
// the share page's 撤销共享, then the server's name and hosts for the share
// list. Both are idempotent, so a retried step is safe.
func (a *app) revokeFLVXShareForDeletion(ctx context.Context, id int64) error {
	db := a.flvxDB()
	if db == nil {
		return errors.New("FLVX 共享存储不可用")
	}
	if err := a.saveFLVXShare(ctx, id, flvx.Config{}, "revoked", nil); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	return flvxTransaction(ctx, db, func(tx *sql.Tx) error {
		s, err := readFLVXShare(ctx, tx, id, true)
		if errors.Is(err, sql.ErrNoRows) || err == nil && s.Deleted != nil {
			return nil
		}
		if err != nil {
			return err
		}
		d := &flvxDeletedServer{At: time.Now().UnixMilli()}
		var raw []byte
		err = tx.QueryRowContext(ctx, `SELECT to_jsonb(s) FROM remote_servers s WHERE id=$1`, id).Scan(&raw)
		if err == nil {
			data := map[string]any{}
			if err = json.Unmarshal(raw, &data); err != nil {
				return err
			}
			d.Name, d.Hosts = deletionText(data["name"]), flvxLinkHosts(data)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		s.Deleted = d
		return writeFLVXShare(ctx, tx, id, s)
	})
}

// retireFLVXShares stamps when FLVX first listed a deleted server's share and
// drops expired ones. It never fails the list; a failed write is retried on
// the next listing.
func retireFLVXShares(ctx context.Context, db *sql.DB, seen, expired []int64, now time.Time) {
	if len(seen) == 0 && len(expired) == 0 {
		return
	}
	err := flvxTransaction(ctx, db, func(tx *sql.Tx) error {
		for _, id := range seen {
			s, err := readFLVXShare(ctx, tx, id, true)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			if s.Deleted != nil && s.Deleted.SeenAt == 0 {
				s.Deleted.SeenAt = now.UnixMilli()
				if err = writeFLVXShare(ctx, tx, id, s); err != nil {
					return err
				}
			}
		}
		for _, id := range expired {
			s, err := readFLVXShare(ctx, tx, id, true)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			if s.Deleted == nil || !flvxRetiredShareExpired(s.Deleted, now) {
				continue
			}
			for _, query := range []string{`DELETE FROM mmwxc_flvx_counters WHERE server_id=$1 AND NOT EXISTS(SELECT 1 FROM remote_servers WHERE id=$1)`, `DELETE FROM mmwxc_flvx_shares WHERE server_id=$1 AND NOT EXISTS(SELECT 1 FROM remote_servers WHERE id=$1)`} {
				if _, err = tx.ExecContext(ctx, query, id); err != nil {
					return err
				}
			}
			log.Printf("[mmwx-custom] flvx share server_id=%d dropped: server deleted, retention over", id)
		}
		return nil
	})
	if err != nil {
		logFLVXShareSkip(0, "retire deleted-server shares failed: "+err.Error())
	}
}

var flvxShareSkipLog = struct {
	sync.Mutex
	at map[int64]time.Time
}{at: map[int64]time.Time{}}

// logFLVXShareSkip logs at most once an hour per server: FLVX polls the list
// every few seconds.
func logFLVXShareSkip(id int64, reason string) {
	flvxShareSkipLog.Lock()
	defer flvxShareSkipLog.Unlock()
	if last, ok := flvxShareSkipLog.at[id]; ok && time.Since(last) < time.Hour {
		return
	}
	flvxShareSkipLog.at[id] = time.Now()
	log.Printf("[mmwx-custom] flvx share server_id=%d not listed: %s", id, reason)
}
