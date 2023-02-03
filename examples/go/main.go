package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	realm "github.com/steviebps/realm/pkg"
)

type CustomStruct struct {
	Foo string `json:"foo,omitempty"`
}

func main() {
	var err error
	rlm := realm.NewRealm(realm.RealmOptions{})
	rlm.SetVersion("v1.0.0")

	if err := rlm.AddConfigPath("./"); err != nil {
		log.Fatal(err)
	}

	if err := rlm.SetConfigName("chambers.json"); err != nil {
		log.Fatal(err)
	}

	if err := rlm.ReadInConfig(true); err != nil {
		log.Fatal(err)
	}

	port, _ := rlm.Float64Value("port", 3000)

	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		message, _ := rlm.StringValue("message", "DEFAULT")
		w.Write([]byte(message))
	})

	mux.HandleFunc("/custom", func(w http.ResponseWriter, r *http.Request) {
		var custom *CustomStruct

		if err := rlm.CustomValue("custom", &custom); err != nil {
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
