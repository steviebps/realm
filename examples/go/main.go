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
	realmhttp "github.com/steviebps/realm/http"
	realm "github.com/steviebps/realm/pkg"
)

type CustomStruct struct {
	Foo string `json:"foo,omitempty"`
}

func main() {
	var err error

	client, err := client.NewClient(&client.ClientConfig{Address: "http://localhost:8080"})
	if err != nil {
		log.Fatal(err)
	}

	rlm, err := realm.NewRealm(realm.WithClient(client), realm.WithVersion("v1.0.0"), realm.WithPath("root"), realm.WithRefreshInterval(1*time.Minute))
	if err != nil {
		log.Fatal(err)
	}
	err = rlm.Start()
	if err != nil {
		log.Fatal(err)
	}

	bootCtx := rlm.NewContext(context.Background())
	port, _ := rlm.Float64(bootCtx, "port", 3000)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		message, _ := rlm.String(r.Context(), "message", "DEFAULT")
		w.Write([]byte(message))
	})

	mux.HandleFunc("/custom", func(w http.ResponseWriter, r *http.Request) {
		var custom *CustomStruct
		if err := rlm.CustomValue(r.Context(), "custom", &custom); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(custom)
	})

	rlmHandler := realmhttp.RealmHandler(rlm, mux)

	server := &http.Server{Addr: fmt.Sprintf(":%d", int(port)), Handler: rlmHandler}

	go func() {
		log.Println("Listening on :", port)
		if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	rlm.Stop()
	if err := server.Shutdown(bootCtx); err != nil {
		log.Fatal(err.Error())
	}
}
