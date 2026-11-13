package idempotency

import (
	"errors"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newDB(t *testing.T) *gorm.DB {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&ProcessedEvent{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestOnceRunsOnceOnReplay(t *testing.T) {
	db := newDB(t)
	n := 0
	for i := 0; i < 3; i++ {
		if err := Once(db, "sale:1", "b1", func(*gorm.DB) error { n++; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if n != 1 {
		t.Fatalf("handler ran %d times", n)
	}
}

func TestOnceRollsBackClaimOnError(t *testing.T) {
	db := newDB(t)
	n := 0
	if err := Once(db, "k", "b1", func(*gorm.DB) error { n++; return errors.New("boom") }); err == nil {
		t.Fatal("want error")
	}
	if err := Once(db, "k", "b1", func(*gorm.DB) error { n++; return nil }); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("redelivery did not rerun: %d", n)
	}
}

func TestEventKeyStable(t *testing.T) {
	a := EventKey("t", "x", 1, []byte(`{"a":1}`))
	if a != EventKey("t", "x", 1, []byte(`{"a":1}`)) || a == EventKey("t", "x", 2, []byte(`{"a":1}`)) {
		t.Fatal("key not stable or not distinct")
	}
}
