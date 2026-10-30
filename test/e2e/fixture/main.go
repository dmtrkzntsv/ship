// Command fixture is the service ship's end-to-end test publishes and
// installs. It is built once per test version with
//
//	-ldflags "-X main.version=<v>"          (and -X main.broken=1 for a bad build)
//
// Subcommands:
//
//	serve      write proof files, then serve the version over HTTP on
//	           127.0.0.1:$PORT (or just wait for SIGTERM when PORT is empty);
//	           a broken build exits 1 at once
//	version    print the version
//	echo ARGS  print ARGS (used by `ship run`)
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var (
	version = "dev"
	broken  = ""
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: fixture serve|version|echo [args...]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		if err := serve(); err != nil {
			fmt.Fprintln(os.Stderr, "fixture:", err)
			os.Exit(1)
		}
	case "version":
		fmt.Println(version)
	case "echo":
		fmt.Println(strings.Join(os.Args[2:], " "))
	default:
		fmt.Fprintf(os.Stderr, "fixture: unknown command %q\n", os.Args[1])
		os.Exit(2)
	}
}

// serve writes $DATA/served (the version) and, when RUN is set,
// $RUN/$NAME.ready, then serves until SIGTERM.
func serve() error {
	if broken != "" {
		return fmt.Errorf("%s is a deliberately broken build", version)
	}
	if data := os.Getenv("DATA"); data != "" {
		if err := os.WriteFile(filepath.Join(data, "served"), []byte(version+"\n"), 0o644); err != nil {
			return err
		}
	}
	if run := os.Getenv("RUN"); run != "" {
		if err := os.WriteFile(filepath.Join(run, os.Getenv("NAME")+".ready"), []byte(version+"\n"), 0o644); err != nil {
			return err
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	port := os.Getenv("PORT")
	if port == "" {
		<-ctx.Done()
		return nil
	}
	srv := &http.Server{
		Addr:              net.JoinHostPort("127.0.0.1", port),
		ReadHeaderTimeout: 5 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprintln(w, version)
		}),
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
