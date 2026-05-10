package config

import (
	"log"
	"os"

	"github.com/joho/godotenv"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type BaseConfig interface {
	LoadEnv(filenames ...string)
	IsProduction() bool
	IsDevelopment() bool
	IsTesting() bool
	GetDBConfig() *gorm.Config
	PaginateConfig() paginate.Config
}

type baseConfig struct{}

func NewBaseConfig() BaseConfig {
	return &baseConfig{}
}

func (c *baseConfig) LoadEnv(filenames ...string) {
	if err := godotenv.Load(filenames...); err != nil {
		log.Println("Warning: .env file not found or failed to load")
	} else {
		log.Println(".env loaded successfully")
	}
}

func (c *baseConfig) IsProduction() bool {
	return os.Getenv("APP.ENV") == "production"
}

func (c *baseConfig) IsDevelopment() bool {
	return os.Getenv("APP.ENV") == "development" || os.Getenv("APP.ENV") == ""
}

func (c *baseConfig) IsTesting() bool {
	return os.Getenv("APP.ENV") == "testing"
}

func (c *baseConfig) GetDBConfig() *gorm.Config {
	logLevel := logger.Info
	envLogLevel := os.Getenv("DB_LOG_LEVEL")
	switch envLogLevel {
	case "silent":
		logLevel = logger.Silent
	case "error":
		logLevel = logger.Error
	case "warn":
		logLevel = logger.Warn
	case "info":
		logLevel = logger.Info
	default:
		if c.IsProduction() && envLogLevel == "" {
			logLevel = logger.Silent
		}
	}

	return &gorm.Config{
		Logger:         logger.Default.LogMode(logLevel),
		TranslateError: true,
	}
}

func (c *baseConfig) PaginateConfig() paginate.Config {
	return paginate.Config{
		DefaultSize:          10,
		FieldSelectorEnabled: true,
		ErrorEnabled:         true,
	}
}
