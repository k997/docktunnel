package logger

import (
	"testing"
)

func TestLoggerCreation(t *testing.T) {
	// 测试创建不同配置的日志器
	logger := New("info", "text")
	if logger == nil {
		t.Error("Failed to create logger")
	}

	jsonLogger := New("debug", "json")
	if jsonLogger == nil {
		t.Error("Failed to create JSON logger")
	}
}

func TestLoggerLevels(t *testing.T) {
	// 测试不同日志级别
	debugLogger := New("debug", "text")
	if debugLogger == nil {
		t.Error("Failed to create debug logger")
	}

	infoLogger := New("info", "text")
	if infoLogger == nil {
		t.Error("Failed to create info logger")
	}

	warnLogger := New("warn", "text")
	if warnLogger == nil {
		t.Error("Failed to create warn logger")
	}

	errorLogger := New("error", "text")
	if errorLogger == nil {
		t.Error("Failed to create error logger")
	}

	// 测试默认级别（无效级别应该回退到info）
	defaultLogger := New("invalid", "text")
	if defaultLogger == nil {
		t.Error("Failed to create default logger")
	}
}

func TestLoggerFormats(t *testing.T) {
	// 测试不同输出格式
	textLogger := New("info", "text")
	if textLogger == nil {
		t.Error("Failed to create text logger")
	}

	jsonLogger := New("info", "json")
	if jsonLogger == nil {
		t.Error("Failed to create JSON logger")
	}

	// 测试默认格式（无效格式应该回退到text）
	defaultLogger := New("info", "invalid")
	if defaultLogger == nil {
		t.Error("Failed to create default format logger")
	}
}
