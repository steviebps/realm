package main

import (
	"fmt"
	"log"
	"net/http"

	realm "github.com/steviebps/realm/pkg"
)

func main() {

	realm.SetVersion("v1.0.0")

	if err := realm.AddConfigPath("./"); err != nil {
		log.Fatal(err)
	}

	if err := realm.SetConfigName("chambers.json"); err != nil {
		log.Fatal(err)
	}

	if err := realm.ReadInConfig(true); err != nil {
		log.Fatal(err)
	}

	port, _ := realm.Float64Value("port", 3000)

	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		message, _ := realm.StringValue("message", "DEFAULT")
		w.Write([]byte(message))
	})

	log.Println("Listening on :", port)
	err := http.ListenAndServe(fmt.Sprintf(":%d", int(port)), mux)
	if err != nil {
		log.Fatal(err)
	}
}
