# realm

[![release](https://github.com/steviebps/realm/actions/workflows/go.yml/badge.svg)](https://github.com/steviebps/realm/actions/workflows/go.yml)

```go install github.com/steviebps/realm```


## starter configs

### a basic chamber file
```wget -O $HOME/.realm/masterChamber.json https://raw.githubusercontent.com/steviebps/realm/master/configs/masterChamber.json```


## example commands

### build
```realm build -o /path/to/your/directory```

with forced directory creation

```realm build -o /path/to/your/directory --force```

#### Pipe into an archive: 
```realm build | tar zcf realm.tar.gz -T -```

### print

#### Pretty prints your global chamber to stdout:
```realm print -p```

#### Print your global chamber to file:
```realm print -o /path/to/your/file.json```


## example code snippets

```go
import (
	"fmt"
	"log"
	"net/http"


	realm "github.com/steviebps/realm/pkg"
)


func main() {
	// because realm configurations contain overrides based on the version of your application, specify it here
	realm.SetVersion("v1.0.0")

  	// tell realm where to look for realm configuration
	if err := realm.AddConfigPath("./"); err != nil {
		log.Fatal(err)
	}

  	// tell realm what file name it should look for in the specified paths
	if err := realm.SetConfigName("chambers.json"); err != nil {
		log.Fatal(err)
	}

 	// look for and read in the realm configuration
  	// passing "true" will tell realm to watch the file for changes
	if err := realm.ReadInConfig(true); err != nil {
		log.Fatal(err)
	}

  	// return a float64 value from the config and specify a default value if it does not exist
	port, _ := realm.Float64Value("port", 3000)
  
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
    		// retrieve a string value from your realm config and specify a default value if it does not exist
		message, _ := realm.StringValue("message", "DEFAULT")
		w.Write([]byte(message))
	})
  
	log.Println("Listening on :", port)
	err := http.ListenAndServe(fmt.Sprintf(":%d", int(port)), mux)
	if err != nil {
		log.Fatal(err)
	}
}
```

