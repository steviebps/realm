package main

import (
	"fmt"
	"log"
	"net/http"

	rein "github.com/steviebps/rein/pkg"
)

func handler(w http.ResponseWriter, r *http.Request) {
	log.Println("Executing handler:", r.RemoteAddr)
	w.Write([]byte("OK"))
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", handler)

	rein.SetVersion("v1.0.0")

	if err := rein.AddConfigPath("./chambers.json"); err != nil {
		log.Fatal(err)
	}

	if err := rein.ReadInConfig(); err != nil {
		log.Fatal(err)
	}

	port := rein.Float64Value("port", 3000)

	log.Println("Listening on :", port)
	err := http.ListenAndServe(fmt.Sprintf(":%d", int(port)), mux)
	if err != nil {
		log.Fatal(err)
	}
}
