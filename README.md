# realm

[![release](https://github.com/steviebps/realm/actions/workflows/go.yml/badge.svg)](https://github.com/steviebps/realm/actions/workflows/go.yml)

```go  get -u github.com/steviebps/realm```


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
