# realm

[![release](https://github.com/steviebps/realm/actions/workflows/go.yml/badge.svg)](https://github.com/steviebps/realm/actions/workflows/go.yml)

```bash
go install github.com/steviebps/realm
```

## example commands

### server

#### start a local realm server

```bash
realm server --config ./configs/realm.json
```

## example code snippets

```go
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

	// create a realm client for retrieving your chamber from your local or remote host
	client, err := client.NewClient(&client.ClientConfig{Address: "http://localhost"})
	if err != nil {
		log.Fatal(err)
	}

	// initialize your realm 
	rlm, err := realm.NewRealm(realm.RealmOptions{Client: client, ApplicationVersion: "v1.0.0", Path: "root"})
	if err != nil {
		log.Fatal(err)
	}

	// start fetching your chamber from the local or remote host
	err = rlm.Start()
	if err != nil {
		log.Fatal(err)
	}

	// create a realm context
	bootCtx := rlm.NewContext(context.Background())
	// retrieve your first config value
	port, _ := rlm.Float64(bootCtx, "port", 3000)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// retrieve the message value with a new context
		// note: use the same context value throughout the request for consistency
		message, _ := rlm.String(rlm.NewContext(r.Context()), "message", "DEFAULT")
		w.Write([]byte(message))
	})

	mux.HandleFunc("/custom", func(w http.ResponseWriter, r *http.Request) {
		var custom *CustomStruct
		// retrieve a custom value and unmarshal it
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
```


