package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/steviebps/realm/client"
	realm "github.com/steviebps/realm/pkg"
	realmtrace "github.com/steviebps/realm/trace"
)

type CustomStruct struct {
	Foo string `json:"foo,omitempty"`
}

func main() {
	ctx := context.Background()
	shutdownFn, err := realmtrace.SetupOtelInstrumentation(ctx, false, "v1.0.0")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = shutdownFn(ctx) }()

	client, err := client.NewHttpClient(&client.HttpClientConfig{Address: "http://localhost:8080", Token: os.Getenv("REALM_TOKEN")})
	if err != nil {
		log.Fatal(err)
	}

	rlm, err := realm.NewRealm(realm.WithHttpClient(client), realm.WithVersion("v1.0.0"), realm.WithPath("/a new thing"), realm.WithPollingInterval(1*time.Minute))
	if err != nil {
		log.Fatal(err)
	}
	// Start keeps retrying in the background when the first load fails, so
	// serve the flags' defaults until realm is reachable rather than exiting.
	if err := rlm.Start(); err != nil {
		log.Printf("realm is not reachable yet; serving default flag values until it is: %v", err)
	}
	defer rlm.Stop()

	bootCtx := rlm.NewContext(ctx)
	port, _ := rlm.Float64(bootCtx, "port", 3000)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		message, _ := rlm.String(r.Context(), "message", "DEFAULT")
		// plain text: a flag value is data, never markup
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(message))
	})

	mux.HandleFunc("/custom", func(w http.ResponseWriter, r *http.Request) {
		var custom *CustomStruct
		if err := rlm.CustomValue(r.Context(), "custom", &custom); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(custom)
	})

	// Pin one snapshot of the flags to each request, so that every flag read
	// while serving it agrees.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(rlm.NewContext(r.Context())))
	})

	server := &http.Server{Addr: fmt.Sprintf(":%d", int(port)), Handler: handler, ReadHeaderTimeout: 10 * time.Second}

	go func() {
		log.Println("Listening on :", port)
		if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}
