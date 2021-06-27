# rein

[![release](https://github.com/steviebps/rein/actions/workflows/go.yml/badge.svg)](https://github.com/steviebps/rein/actions/workflows/go.yml)

```go  get -u github.com/steviebps/rein```


## starter configs

### rein yaml for where your chambers are stored
```echo "chamber: $HOME/.rein/masterChamber.json" > "$HOME/.rein/rein.yaml"```

### a basic chamber file
```wget -O $HOME/.rein/masterChamber.json https://raw.githubusercontent.com/steviebps/rein/master/configs/masterChamber.json```


## example commands

### build
```rein build -o /path/to/your/directory```

with forced directory creation

```rein build -o /path/to/your/directory --force```

#### Pipe into an archive: 
```rein build | tar zcf archive.tar.gz -T -```

### print

#### Pretty prints your global chamber to stdout:
```rein print -p```

#### Print your global chamber to file:
```rein print -o /path/to/your/file.json```
