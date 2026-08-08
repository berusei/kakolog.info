// kakoapi は VPS で常駐する検索 API サーバー（仕様書10章）。
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"kakosearch/internal/config"
	"kakosearch/internal/index"
	"kakosearch/internal/store"
	"kakosearch/internal/tokenizer"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	cfg := config.Load()

	// 本番 SQLite は読み取り専用で開く（仕様書8.2）
	db, err := store.Open(cfg.DBPath, true)
	if err != nil {
		log.Fatalf("SQLite: %v", err)
	}
	defer db.Close()

	// 正規化バージョン照合。不一致なら起動拒否（仕様書16.3 MUST）
	if err := checkNormalizerVersion(db, tokenizer.Version); err != nil {
		log.Fatalf("起動拒否: %v", err)
	}

	search, err := index.Open(cfg.ManticoreAddr)
	if err != nil {
		log.Fatalf("Manticore: %v", err)
	}
	defer search.Close()

	srv, err := newServer(cfg, db, search)
	if err != nil {
		log.Fatalf("初期化失敗: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go srv.refreshLoop(ctx)

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      15 * time.Second,
	}
	go func() {
		log.Printf("kakoapi listening on %s (db=%s manticore=%s hasOld=%v)",
			cfg.Listen, cfg.DBPath, cfg.ManticoreAddr, cfg.HasOld)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	log.Print("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
	os.Exit(0)
}
