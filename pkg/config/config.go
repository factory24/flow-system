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
	if c.IsProduction() {
		logLevel = logger.Silent
	}

	return &gorm.Config{
		Logger:         logger.Default.LogMode(logLevel),
		TranslateError: true,
	}
}
