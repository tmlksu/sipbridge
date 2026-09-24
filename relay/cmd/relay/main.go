// Command relay は sipbridge の変換装置である。Asterisk には SIP 内線として
// 振る舞い、アプリには WebSocket (wss://<host>/v1/session) を提供する。
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/tmlksu/sipbridge/relay/internal/auth"
	"github.com/tmlksu/sipbridge/relay/internal/call"
	"github.com/tmlksu/sipbridge/relay/internal/config"
	"github.com/tmlksu/sipbridge/relay/internal/fakebackend"
	"github.com/tmlksu/sipbridge/relay/internal/push"
	"github.com/tmlksu/sipbridge/relay/internal/session"
	"github.com/tmlksu/sipbridge/relay/internal/sipbackend"
	"github.com/tmlksu/sipbridge/relay/internal/state"
)

// RelayVersion は hello.relayVersion として通知する。
const RelayVersion = "0.2.0"

// shutdownGrace は SIGTERM 後に各 account の登録解除 (REGISTER Expires: 0)
// を送り切るための猶予である。
const shutdownGrace = 2 * time.Second

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(runHealthcheck())
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "relay:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel)

	// 状態ファイル (account / 端末結び付け / push トークン)。
	// 読み込めなくても起動は継続する (警告のみ、メモリのみで動作)。
	store, err := state.New(cfg.StateFile)
	if err != nil {
		log.Warn("状態ファイルの読み込みに失敗。メモリのみで継続", "err", err, "file", cfg.StateFile)
		store, _ = state.New("")
	} else if store.Migrated() {
		log.Info("旧形式の状態ファイルを version 2 へ移行した", "file", cfg.StateFile)
	}

	// account ごとの Backend を作るファクトリ。BACKEND で実装を切り替える。
	var factory session.BackendFactory
	switch cfg.Backend {
	case "fake":
		factory = func(user, password, display string) (call.Backend, error) {
			return fakebackend.New(), nil
		}
		log.Info("バックエンド: fake (テスト用)")
	case "sip":
		factory = func(user, password, display string) (call.Backend, error) {
			return sipbackend.New(sipbackend.Config{
				SIPHost:    cfg.SIPHost,
				SIPPort:    cfg.SIPPort,
				User:       user,
				Password:   password,
				Display:    display,
				LocalIP:    cfg.LocalIP,
				RTPPortMin: cfg.RTPPortMin,
				RTPPortMax: cfg.RTPPortMax,
			}, log)
		}
		log.Info("バックエンド: sip", "host", cfg.SIPHost, "port", cfg.SIPPort)
	default:
		return fmt.Errorf("未知の BACKEND %q", cfg.Backend)
	}

	var authn auth.Authenticator
	switch cfg.AuthMode {
	case "token":
		authn = auth.NewTokenAuth(cfg.DevToken)
		log.Info("認証: 開発用共有トークン")
	case "cf-access":
		authn = auth.NewCFAccessAuth(cfg.CFTeamDomain, cfg.CFAccessAUD)
		log.Info("認証: Cloudflare Access JWT", "team", cfg.CFTeamDomain)
	}

	// push 送信器: FCM 設定があれば FCM、無ければ no-op。
	var pusher push.Pusher = push.Noop{}
	if cfg.FCMProjectID != "" && cfg.FCMServiceAccountFile != "" {
		fcm, err := push.NewFCM(push.FCMConfig{
			ProjectID:          cfg.FCMProjectID,
			ServiceAccountFile: cfg.FCMServiceAccountFile,
			Store:              store, // UNREGISTERED トークンの自動削除先
		})
		if err != nil {
			return fmt.Errorf("FCM 初期化失敗: %w", err)
		}
		pusher = fcm
		log.Info("push: FCM 有効", "project", cfg.FCMProjectID)
	} else {
		log.Info("push: 無効 (FCM_PROJECT_ID / FCM_SERVICE_ACCOUNT_FILE 未設定)")
	}

	hub := session.NewHub(factory, pusher, store, session.Config{
		Version:         RelayVersion,
		DefaultAccount:  cfg.SIPUser,
		DefaultPassword: cfg.SIPPassword,
		DefaultDisplay:  cfg.SIPDisplay,
		ResumeTimeout:   time.Duration(cfg.ResumeTimeoutSec) * time.Second,
	}, log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := hub.Run(ctx); err != nil {
		return err
	}

	mux := buildMux(authn, hub)

	// ReadTimeout/WriteTimeout は WS の長時間接続を切ってしまうため設定しない。
	// ヘッダを送り終わらない接続とアイドル接続だけを刈る (gosec G112)。
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shCtx)
	}()
	log.Info("relay 起動", "listen", cfg.Listen,
		"accounts", hub.AccountCount(), "defaultAccount", cfg.SIPUser, "state", cfg.StateFile)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	// 各 account の登録解除 (REGISTER Expires: 0) を送り切る猶予を取る。
	if ctx.Err() != nil {
		log.Info("停止処理 (SIP 登録解除待ち)", "grace", shutdownGrace.String())
		time.Sleep(shutdownGrace)
	}
	return nil
}

// buildMux は HTTP ルーティングを組み立てる。/healthz は認証不要、
// /v1/session は認証必須である。
func buildMux(authn auth.Authenticator, hub *session.Hub) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/v1/session", func(w http.ResponseWriter, r *http.Request) {
		if err := authn.Authenticate(r); err != nil {
			slog.Warn("WS 認証失敗", "err", err)
			http.Error(w, "unauthorized: "+err.Error(), http.StatusUnauthorized)
			return
		}
		hub.ServeWS(w, r)
	})
	return mux
}

func newLogger(level string) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lv}))
}

// runHealthcheck は LISTEN の /healthz を叩く (Docker HEALTHCHECK 用)。
// 200 なら exit 0、それ以外/接続失敗は exit 1。
func runHealthcheck() int {
	cfg, err := config.FromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	host, port, err := splitListen(cfg.Listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	url := "http://" + net.JoinHostPort(host, port) + "/healthz"
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck: /healthz が %d\n", resp.StatusCode)
		return 1
	}
	return 0
}

// splitListen は "host:port" または ":port" を分解する。
// ホスト部が空・ワイルドカードの場合は 127.0.0.1 を使う。
func splitListen(listen string) (string, string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", "", fmt.Errorf("LISTEN %q の解析失敗: %w", listen, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	// "localhost" のままでは解決に依存するため 127.0.0.1 に寄せる。
	if host == "localhost" {
		host = "127.0.0.1"
	}
	return host, port, nil
}
