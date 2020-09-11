# rein

```go  get -u github.com/steviebps/rein```


## starter configs

### rein yaml for where your chambers are stored
```echo "chamber: /your/file/location/masterChamber.json" > "$HOME/.rein.yaml"```

### a basic chamber file
```wget -O /your/file/location/masterChamber.json https://raw.githubusercontent.com/steviebps/rein/master/configs/masterChamber.json```



## example commands

### build chambers
```rein build -o /path/to/your/directory```

Pipe into an archive: 
```rein build | tar zcf archive.tar.gz -T -```
