# go-dl
A small Go package to download files.

## Usage
```sh
go get github.com/opxyc/go-dl/dl
```
```go
import "github.com/opxyc/go-dl/dl"
```
```go
d := dl.New("http://stackoverflow.design/assets/img/logos/so/logo-stackoverflow.svg", "./downloads", "stackoverflow-logo.svg", 1)

// get the size of file in bytes
n, err := d.Size()
// ...

// download the file
ch := make(chan int64) // channel to get the download progress
err = d.Do(ch)
// ...

// get time taken to complete the download
duration := d.Duration()
// ...
```
A detailed example is available in [**cmd**](./cmd/main.go).