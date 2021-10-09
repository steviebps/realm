package main

import (
	"fmt"
	"log"
	"net/http"

	realm "github.com/steviebps/realm/pkg"
)

func handler(w http.ResponseWriter, r *http.Request) {
	message := realm.StringValue("message", "DEFAULT")
	w.Write([]byte(message))
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", handler)

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

	port := realm.Float64Value("port", 3000)

	log.Println("Listening on :", port)
	err := http.ListenAndServe(fmt.Sprintf(":%d", int(port)), mux)
	if err != nil {
		log.Fatal(err)
	}
}
