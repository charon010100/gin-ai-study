// Package logger 是最小日志封装。
//
// 为什么还要包一层 log 而不是直接用标准库：
//   - 想让「输出目标」和「前缀」在一处配置
//   - 想让业务代码统一调用 log.Info 而不是零散的 fmt.Printf / log.Printf
//
// 但本步刻意不引入 zap / slog 这类结构化日志库。
// 理由：Step 9 的目的是让你看清分层与依赖方向，
// 多一个日志库只会分散注意力。真实项目的日志选型见 Step 10。
package logger

import (
	"log"
	"os"
)

// Level 是日志级别。
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// Logger 是日志器。
//
// 用结构体而不是包级全局变量，理由与配置相同：
// 全局状态让测试无法隔离，也让「同一进程两套日志」不可能。
// 它通过依赖注入传给需要的组件。
type Logger struct {
	level  Level
	logger *log.Logger
}

// New 构造日志器。
func New(level Level) *Logger {
	return &Logger{
		level: level,
		// 标准库 log 的输出带时间和文件名，
		// 对教学项目足够；生产环境的取舍见 Step 10。
		logger: log.New(os.Stdout, "", log.LstdFlags),
	}
}

// Debug 输出调试日志。
func (l *Logger) Debug(format string, args ...any) {
	if l.level <= LevelDebug {
		l.logger.Printf("[DEBUG] "+format, args...)
	}
}

// Info 输出普通日志。
func (l *Logger) Info(format string, args ...any) {
	if l.level <= LevelInfo {
		l.logger.Printf("[INFO] "+format, args...)
	}
}

// Warn 输出警告日志。
func (l *Logger) Warn(format string, args ...any) {
	if l.level <= LevelWarn {
		l.logger.Printf("[WARN] "+format, args...)
	}
}

// Error 输出错误日志。
func (l *Logger) Error(format string, args ...any) {
	if l.level <= LevelError {
		l.logger.Printf("[ERROR] "+format, args...)
	}
}
