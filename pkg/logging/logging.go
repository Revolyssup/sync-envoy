package logging

import (
	"log"
	"strings"
)

// Log levels
const (
	LevelDebug = iota
	LevelInfo
	LevelWarn
	LevelError
)

var currentLevel = LevelInfo

var levelNames = map[string]int{
	"debug": LevelDebug,
	"info":  LevelInfo,
	"warn":  LevelWarn,
	"error": LevelError,
}

// SetLevel sets the log level from a string name.
// Returns false if the level name is invalid.
func SetLevel(name string) bool {
	lvl, ok := levelNames[strings.ToLower(name)]
	if !ok {
		return false
	}
	currentLevel = lvl
	return true
}

// GetLevel returns the current log level.
func GetLevel() int {
	return currentLevel
}

func Debug(format string, v ...interface{}) {
	if currentLevel <= LevelDebug {
		log.Printf("[DEBUG] "+format, v...)
	}
}

func Info(format string, v ...interface{}) {
	if currentLevel <= LevelInfo {
		log.Printf("[INFO] "+format, v...)
	}
}

func Warn(format string, v ...interface{}) {
	if currentLevel <= LevelWarn {
		log.Printf("[WARN] "+format, v...)
	}
}

func Errorf(format string, v ...interface{}) {
	if currentLevel <= LevelError {
		log.Printf("[ERROR] "+format, v...)
	}
}
