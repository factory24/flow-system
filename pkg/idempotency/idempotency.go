package idempotency

import (
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ProcessedEvent records one handled event key; embed its table in the service migration list.
type ProcessedEvent struct {
	Key        string `gorm:"primaryKey"`
	BusinessID string `gorm:"index;not null"`
	CreatedAt  time.Time
}

func (ProcessedEvent) TableName() string { return "processed_events" }

// Claim records key once and reports whether this call was the first.
func Claim(db *gorm.DB, key, business string) (bool, error) {
	res := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&ProcessedEvent{Key: key, BusinessID: business})
	return res.RowsAffected == 1, res.Error
}

// Once runs fn in a transaction only for the first delivery of key. A replay is a no-op, and a failed fn
// rolls the claim back so the redelivery runs again.
func Once(db *gorm.DB, key, business string, fn func(tx *gorm.DB) error) error {
	return db.Transaction(func(tx *gorm.DB) error {
		first, err := Claim(tx, key, business)
		if err != nil || !first {
			return err
		}
		return fn(tx)
	})
}
