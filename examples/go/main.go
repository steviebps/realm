package main

import (
	"fmt"
	"log"
	"net/http"

	rein "github.com/steviebps/rein/pkg"
)

func main() {
	fs := http.FileServer(http.Dir("./static"))
	http.Handle("/", fs)

	rein.SetVersion("v1.0.0")

	if err := rein.AddConfigPath("./chambers.json"); err != nil {
		log.Fatal(err)
	}

	if err := rein.ReadInConfig(); err != nil {
		log.Fatal(err)
	}

	port := rein.Float64Value("port", 3000)

	log.Println("Listening on :", port)
	err := http.ListenAndServe(fmt.Sprintf(":%d", int(port)), nil)
	if err != nil {
		log.Fatal(err)
	}
}
