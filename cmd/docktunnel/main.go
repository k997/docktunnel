package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"docktunnel/internal/config"
	"docktunnel/internal/logger"
)

func main() {
	log.Println("DockTunnel starting...")

	// 加载配置
	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// 验证配置
	if err := cfg.Validate(); err != nil {
		log.Fatalf("Invalid config: %v", err)
	}

	// 初始化日志记录器
	logger := logger.New(cfg.GetLogLevel(), cfg.GetLogFormat())
	logger.Info("Configuration loaded successfully")

	// 设置系统信号处理
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// 主循环
	logger.Info("DockTunnel started successfully")
	
	// 等待中断信号
	<-sigChan
	logger.Info("Shutting down DockTunnel...")
}