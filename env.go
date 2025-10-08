package app

import (
	"os"

	"github.com/joho/godotenv"
)

const envDotenv = "DOTENV_PATH"

func init() {
	if dotenv, ok := os.LookupEnv(envDotenv); ok {
		_ = godotenv.Load(dotenv)
	} else {
		_ = godotenv.Load()
	}
}
