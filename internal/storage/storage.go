package storage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type DB struct {
	conn *sql.DB
}

type Subscription struct {
	ID        int64
	ChatID    int64
	FeedURL   string
	Title     string
	CreatedBy int64
	CreatedAt time.Time
}

type JoinEvent struct {
	ChatID    int64
	UserID    int64
	Username  string
	FirstName string
	JoinedAt  time.Time
}

type VerificationChallenge struct {
	ID                    int64
	ChatID                int64
	UserID                int64
	Question              string
	Answer                int
	VerificationMessageID int
	ExpiresAt             time.Time
	CreatedAt             time.Time
}

func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}

	conn, err := sql.Open("sqlite3", path+"?_busy_timeout=5000&_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(1)

	db := &DB{conn: conn}
	if err := db.migrate(context.Background()); err != nil {
		conn.Close()
		return nil, err
	}
	return db, nil
}

func (db *DB) Close() error {
	if db == nil || db.conn == nil {
		return nil
	}
	return db.conn.Close()
}

func (db *DB) migrate(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS rss_subscriptions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id INTEGER NOT NULL,
			feed_url TEXT NOT NULL,
			title TEXT NOT NULL DEFAULT '',
			created_by INTEGER NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(chat_id, feed_url)
		);`,
		`CREATE TABLE IF NOT EXISTS rss_items (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			subscription_id INTEGER NOT NULL,
			item_key TEXT NOT NULL,
			item_link TEXT NOT NULL DEFAULT '',
			published_at TIMESTAMP NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY(subscription_id) REFERENCES rss_subscriptions(id) ON DELETE CASCADE,
			UNIQUE(subscription_id, item_key)
		);`,
		`CREATE TABLE IF NOT EXISTS join_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL,
			username TEXT NOT NULL DEFAULT '',
			first_name TEXT NOT NULL DEFAULT '',
			joined_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS moderation_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL,
			message_id INTEGER NOT NULL,
			reason TEXT NOT NULL,
			action TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS join_challenges (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL,
			question TEXT NOT NULL,
			answer INTEGER NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			expires_at TIMESTAMP NOT NULL,
			resolved_at TIMESTAMP NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE INDEX IF NOT EXISTS join_challenges_pending_idx
			ON join_challenges(chat_id, user_id, expires_at)
			WHERE status = 'pending';`,
	}

	for _, statement := range statements {
		if _, err := db.conn.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	if err := db.ensureColumn(ctx, "join_challenges", "verification_message_id", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if _, err := db.conn.ExecContext(ctx, `DELETE FROM rss_subscriptions WHERE chat_id > 0;`); err != nil {
		return err
	}
	return nil
}

func (db *DB) ensureColumn(ctx context.Context, table string, column string, definition string) error {
	rows, err := db.conn.QueryContext(ctx, "PRAGMA table_info("+table+");")
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name string
		var dataType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if name == column {
			return rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	_, err = db.conn.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+column+" "+definition+";")
	return err
}

func (db *DB) AddSubscription(ctx context.Context, chatID int64, feedURL string, title string, createdBy int64) (Subscription, error) {
	_, err := db.conn.ExecContext(ctx, `
		INSERT OR IGNORE INTO rss_subscriptions (chat_id, feed_url, title, created_by)
		VALUES (?, ?, ?, ?);
	`, chatID, feedURL, title, createdBy)
	if err != nil {
		return Subscription{}, err
	}
	return db.SubscriptionByURL(ctx, chatID, feedURL)
}

func (db *DB) SubscriptionByURL(ctx context.Context, chatID int64, feedURL string) (Subscription, error) {
	row := db.conn.QueryRowContext(ctx, `
		SELECT id, chat_id, feed_url, title, created_by, created_at
		FROM rss_subscriptions
		WHERE chat_id = ? AND feed_url = ?;
	`, chatID, feedURL)
	return scanSubscription(row)
}

func (db *DB) RemoveSubscription(ctx context.Context, chatID int64, feedURL string) (bool, error) {
	result, err := db.conn.ExecContext(ctx, `
		DELETE FROM rss_subscriptions
		WHERE chat_id = ? AND feed_url = ?;
	`, chatID, feedURL)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

func (db *DB) ListSubscriptions(ctx context.Context, chatID int64) ([]Subscription, error) {
	rows, err := db.conn.QueryContext(ctx, `
		SELECT id, chat_id, feed_url, title, created_by, created_at
		FROM rss_subscriptions
		WHERE chat_id = ?
		ORDER BY created_at DESC;
	`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return readSubscriptions(rows)
}

func (db *DB) AllSubscriptions(ctx context.Context) ([]Subscription, error) {
	rows, err := db.conn.QueryContext(ctx, `
		SELECT id, chat_id, feed_url, title, created_by, created_at
		FROM rss_subscriptions
		ORDER BY id ASC;
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return readSubscriptions(rows)
}

func (db *DB) UpdateSubscriptionTitle(ctx context.Context, subscriptionID int64, title string) error {
	_, err := db.conn.ExecContext(ctx, `
		UPDATE rss_subscriptions
		SET title = ?
		WHERE id = ? AND title != ?;
	`, title, subscriptionID, title)
	return err
}

func (db *DB) ItemSeen(ctx context.Context, subscriptionID int64, itemKey string) (bool, error) {
	var exists int
	err := db.conn.QueryRowContext(ctx, `
		SELECT 1 FROM rss_items
		WHERE subscription_id = ? AND item_key = ?
		LIMIT 1;
	`, subscriptionID, itemKey).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (db *DB) MarkItemSent(ctx context.Context, subscriptionID int64, itemKey string, itemLink string, publishedAt *time.Time) error {
	_, err := db.conn.ExecContext(ctx, `
		INSERT OR IGNORE INTO rss_items (subscription_id, item_key, item_link, published_at)
		VALUES (?, ?, ?, ?);
	`, subscriptionID, itemKey, itemLink, publishedAt)
	return err
}

func (db *DB) RecordJoin(ctx context.Context, event JoinEvent) error {
	_, err := db.conn.ExecContext(ctx, `
		INSERT INTO join_events (chat_id, user_id, username, first_name, joined_at)
		VALUES (?, ?, ?, ?, ?);
	`, event.ChatID, event.UserID, event.Username, event.FirstName, event.JoinedAt)
	return err
}

func (db *DB) RecordModeration(ctx context.Context, chatID int64, userID int64, messageID int, reason string, action string) error {
	_, err := db.conn.ExecContext(ctx, `
		INSERT INTO moderation_events (chat_id, user_id, message_id, reason, action)
		VALUES (?, ?, ?, ?, ?);
	`, chatID, userID, messageID, reason, action)
	return err
}

func (db *DB) CreateVerificationChallenge(ctx context.Context, chatID int64, userID int64, question string, answer int, expiresAt time.Time) (VerificationChallenge, error) {
	tx, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		return VerificationChallenge{}, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		UPDATE join_challenges
		SET status = 'replaced', resolved_at = ?
		WHERE chat_id = ? AND user_id = ? AND status = 'pending';
	`, time.Now().UTC(), chatID, userID); err != nil {
		return VerificationChallenge{}, err
	}

	result, err := tx.ExecContext(ctx, `
		INSERT INTO join_challenges (chat_id, user_id, question, answer, expires_at)
		VALUES (?, ?, ?, ?, ?);
	`, chatID, userID, question, answer, expiresAt)
	if err != nil {
		return VerificationChallenge{}, err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return VerificationChallenge{}, err
	}
	if err := tx.Commit(); err != nil {
		return VerificationChallenge{}, err
	}

	return VerificationChallenge{
		ID:        id,
		ChatID:    chatID,
		UserID:    userID,
		Question:  question,
		Answer:    answer,
		ExpiresAt: expiresAt,
		CreatedAt: time.Now().UTC(),
	}, nil
}

func (db *DB) ActiveVerificationChallenge(ctx context.Context, chatID int64, userID int64, now time.Time) (VerificationChallenge, bool, error) {
	row := db.conn.QueryRowContext(ctx, `
		SELECT id, chat_id, user_id, question, answer, verification_message_id, expires_at, created_at
		FROM join_challenges
		WHERE chat_id = ? AND user_id = ? AND status = 'pending' AND expires_at > ?
		ORDER BY id DESC
		LIMIT 1;
	`, chatID, userID, now)

	challenge, err := scanVerificationChallenge(row)
	if errors.Is(err, sql.ErrNoRows) {
		return VerificationChallenge{}, false, nil
	}
	if err != nil {
		return VerificationChallenge{}, false, err
	}
	return challenge, true, nil
}

func (db *DB) MarkVerificationPassed(ctx context.Context, challengeID int64) error {
	_, err := db.conn.ExecContext(ctx, `
		UPDATE join_challenges
		SET status = 'passed', resolved_at = ?
		WHERE id = ? AND status = 'pending';
	`, time.Now().UTC(), challengeID)
	return err
}

func (db *DB) MarkVerificationExpired(ctx context.Context, challengeID int64) error {
	_, err := db.conn.ExecContext(ctx, `
		UPDATE join_challenges
		SET status = 'expired', resolved_at = ?
		WHERE id = ? AND status = 'pending';
	`, time.Now().UTC(), challengeID)
	return err
}

func (db *DB) MarkVerificationFailed(ctx context.Context, challengeID int64) error {
	_, err := db.conn.ExecContext(ctx, `
		UPDATE join_challenges
		SET status = 'failed', resolved_at = ?
		WHERE id = ? AND status = 'pending';
	`, time.Now().UTC(), challengeID)
	return err
}

func (db *DB) SetVerificationMessageID(ctx context.Context, challengeID int64, messageID int) error {
	_, err := db.conn.ExecContext(ctx, `
		UPDATE join_challenges
		SET verification_message_id = ?
		WHERE id = ?;
	`, messageID, challengeID)
	return err
}

func (db *DB) ExpiredVerificationChallenges(ctx context.Context, now time.Time, limit int) ([]VerificationChallenge, error) {
	rows, err := db.conn.QueryContext(ctx, `
		SELECT id, chat_id, user_id, question, answer, verification_message_id, expires_at, created_at
		FROM join_challenges
		WHERE status = 'pending' AND expires_at <= ?
		ORDER BY expires_at ASC
		LIMIT ?;
	`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []VerificationChallenge
	for rows.Next() {
		challenge, err := scanVerificationChallenge(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, challenge)
	}
	return result, rows.Err()
}

type subscriptionScanner interface {
	Scan(dest ...any) error
}

type verificationChallengeScanner interface {
	Scan(dest ...any) error
}

func scanSubscription(scanner subscriptionScanner) (Subscription, error) {
	var sub Subscription
	err := scanner.Scan(&sub.ID, &sub.ChatID, &sub.FeedURL, &sub.Title, &sub.CreatedBy, &sub.CreatedAt)
	return sub, err
}

func scanVerificationChallenge(scanner verificationChallengeScanner) (VerificationChallenge, error) {
	var challenge VerificationChallenge
	err := scanner.Scan(
		&challenge.ID,
		&challenge.ChatID,
		&challenge.UserID,
		&challenge.Question,
		&challenge.Answer,
		&challenge.VerificationMessageID,
		&challenge.ExpiresAt,
		&challenge.CreatedAt,
	)
	return challenge, err
}

func readSubscriptions(rows *sql.Rows) ([]Subscription, error) {
	var result []Subscription
	for rows.Next() {
		sub, err := scanSubscription(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, sub)
	}
	return result, rows.Err()
}
