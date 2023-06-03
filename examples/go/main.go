package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/steviebps/realm/client"
	realm "github.com/steviebps/realm/pkg"
)

type CustomStruct struct {
	Foo string `json:"foo,omitempty"`
}

func main() {
	var err error

	client, err := client.NewClient(&client.ClientConfig{Address: "http://localhost"})
	if err != nil {
		log.Fatal(err)
	}
	rlm, err := realm.NewRealm(realm.RealmOptions{Client: client, ApplicationVersion: "v1.0.0", Path: "root"})
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
		message, _ := rlm.String(rlm.NewContext(r.Context()), "message", "DEFAULT")
		w.Write([]byte(message))
	})

	mux.HandleFunc("/custom", func(w http.ResponseWriter, r *http.Request) {
		var custom *CustomStruct
		if err := rlm.CustomValue(rlm.NewContext(r.Context()), "custom", &custom); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(custom)
	})

	log.Println("Listening on :", port)
	err = http.ListenAndServe(fmt.Sprintf(":%d", int(port)), mux)
	if err != nil {
		log.Fatal(err)
	}
}
