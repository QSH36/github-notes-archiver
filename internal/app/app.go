package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

func Run(args []string, version string) error {
	command := "serve"
	if len(args) > 0 && args[0][0] != '-' {
		command, args = args[0], args[1:]
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	dataDir := flags.String("data-dir", envOr("GNA_DATA_DIR", defaultDataDir()), "数据目录")
	logDir := flags.String("log-dir", envOr("GNA_LOG_DIR", defaultLogDir()), "日志目录")
	listen := flags.String("listen", "", "监听地址")
	if err := flags.Parse(args); err != nil {
		return err
	}
	store, initialToken, err := OpenStore(*dataDir, *logDir)
	if err != nil {
		return err
	}
	switch command {
	case "serve":
		if initialToken != "" {
			fmt.Println("首次管理令牌已写入", filepath.Join(*dataDir, "initial-admin-token"))
		}
		if *listen == "" {
			*listen = store.config.Listen
		}
		return serve(store, *listen, version)
	case "status":
		cfg := store.Config()
		fmt.Printf("版本: %s\n仓库: %d\n监听: %s\n", version, len(cfg.Repositories), cfg.Listen)
		for _, repo := range cfg.Repositories {
			fmt.Printf("- %s enabled=%t health=%s last_sync=%s\n", repo.FullName, repo.Enabled, repo.Health, repo.LastSync.Format(time.RFC3339))
		}
		return nil
	case "doctor":
		return doctor(store)
	case "rotate-token":
		token, err := store.rotateToken()
		if err != nil {
			return err
		}
		fmt.Println(token)
		return nil
	case "version":
		fmt.Printf("github-notes-archiver %s %s/%s\n", version, runtime.GOOS, runtime.GOARCH)
		return nil
	default:
		return fmt.Errorf("未知命令 %q（可用: serve, status, doctor, rotate-token, version）", command)
	}
}

func serve(store *Store, listen, version string) error {
	host, _, err := net.SplitHostPort(listen)
	if err != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		return errors.New("为保护管理界面，只允许监听回环地址")
	}
	server := NewServer(store, version)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go server.runScheduler(ctx)
	httpServer := &http.Server{Addr: listen, Handler: server.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 2 * time.Minute, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 << 10}
	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.ListenAndServe() }()
	store.log("info", "service_started", "", "服务监听 "+listen)
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdown)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func doctor(store *Store) error {
	for _, name := range []string{"git", "ssh", "ssh-keygen"} {
		if _, err := exec.LookPath(name); err != nil {
			return fmt.Errorf("缺少 %s", name)
		}
	}
	if _, err := run(context.Background(), "", nil, "git", "--version"); err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(store.dataDir, ".doctor"), []byte("ok\n"), 0600); err != nil {
		return fmt.Errorf("数据目录不可写: %w", err)
	}
	_ = os.Remove(filepath.Join(store.dataDir, ".doctor"))
	fmt.Println("doctor: ok")
	return nil
}

func defaultDataDir() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(os.TempDir(), "github-notes-archiver")
	}
	return "/var/lib/github-notes-archiver"
}

func defaultLogDir() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(defaultDataDir(), "logs")
	}
	return "/var/log/github-notes-archiver"
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
