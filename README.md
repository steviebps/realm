# chambr

[![release](https://github.com/steviebps/rein/actions/workflows/go.yml/badge.svg)](https://github.com/steviebps/rein/actions/workflows/go.yml)

```go  get -u github.com/steviebps/chambr```


## starter configs

### a basic chamber file
```wget -O $HOME/.chambr/masterChamber.json https://raw.githubusercontent.com/steviebps/rein/master/configs/masterChamber.json```


## example commands

### build
```chambr build -o /path/to/your/directory```

with forced directory creation

```chambr build -o /path/to/your/directory --force```

#### Pipe into an archive: 
```chambr build | tar zcf archive.tar.gz -T -```

### print

#### Pretty prints your global chamber to stdout:
```chambr print -p```

#### Print your global chamber to file:
```chambr print -o /path/to/your/file.json```
