package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/18345174/echoear_cloud/internal/api"
	"github.com/18345174/echoear_cloud/internal/config"
	"github.com/18345174/echoear_cloud/internal/database"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("configuration: %v", err)
	}
	db, err := database.Open(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer db.Close()

	httpServer := &http.Server{
		Addr: fmt.Sprintf(":%d", cfg.HTTPPort), Handler: api.NewServer(db, cfg).Handler(),
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 75 * time.Second,
	}
	go func() {
		log.Printf("EchoEar Cloud listening on %s", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	_ = httpServer.Close()
}
