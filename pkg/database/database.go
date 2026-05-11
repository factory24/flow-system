package database

import (
	"fmt"
	"log"
	"math"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type Config interface {
	GetDBConfig() *gorm.Config
	GetDBType() string
	GetDBName() string
	GetDBHost() string
	GetDBPort() string
	GetDBUser() string
	GetDBPass() string
}

type Connection interface {
	GetEngine() *gorm.DB
	Connect()
	MigrateDB()
}

type gormDB struct {
	engine *gorm.DB
	models []interface{}
	cfg    Config
}

func (db *gormDB) GetEngine() *gorm.DB {
	return db.engine
}

func NewGormDatabase(config Config, models []interface{}) Connection {
	return &gormDB{
		cfg:    config,
		models: models,
	}
}

func (db *gormDB) getDialect() (gorm.Dialector, string, error) {
	var d gorm.Dialector

	dbType := db.cfg.GetDBType()

	switch dbType {
	case "sqlite":
		dbName := fmt.Sprintf("%s.db?parseTime=True", db.cfg.GetDBName())
		d = sqlite.Open(dbName)
	case "postgres":
		dsn := fmt.Sprintf("host=%s user=%s password=%s dbname=%s port=%s",
			db.cfg.GetDBHost(),
			db.cfg.GetDBUser(),
			db.cfg.GetDBPass(),
			db.cfg.GetDBName(),
			db.cfg.GetDBPort(),
		)
		d = postgres.Open(dsn)
	default:
		return nil, "", fmt.Errorf("unsupported database type: %s", dbType)
	}

	return d, dbType, nil
}

func (db *gormDB) Connect() {
	var counts int64
	var backOff = 1 * time.Second
	var connection *gorm.DB

	dialect, dbType, err := db.getDialect()
	if err != nil {
		log.Fatalf("failed to get db dialect: %v", err)
	}

	for {
		c, err := gorm.Open(dialect, db.cfg.GetDBConfig())
		if err != nil {
			log.Printf("%s DB not yet ready to connect! Attempt %d", dbType, counts+1)
			counts++
		} else {
			connection = c
			break
		}

		if counts > 5 {
			log.Fatalf("failed to connect to %s database after 5 attempts: %v", dbType, err)
		}

		backOff = time.Duration(math.Pow(float64(counts), 2)) * time.Second
		log.Printf("Backing off for %v...", backOff)
		time.Sleep(backOff)
	}

	log.Printf("%s database connected successfully\n", dbType)

	db.engine = connection
	db.MigrateDB()
}

func (db *gormDB) MigrateDB() {
	if len(db.models) > 0 {
		if err := db.engine.AutoMigrate(db.models...); err != nil {
			log.Fatalf("migration failed: %v", err)
		}
		log.Println("tables migrated successfully!")
	}
}
