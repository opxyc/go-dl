package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/opxyc/go-dl/dl"
)

func main() {
	url := flag.String("url", "", "URL to download from")
	path := flag.String("path", ".", "path in which the downloaded file is to be saved")
	name := flag.String("name", "", "name to save the file with")
	c := flag.Int("chunks", 1, "number of chunks in which the file is to be downloaded")
	flag.Parse()

	if *url == "" || *c <= 0 {
		flag.Usage()
		return
	}

	_, err := os.Stat(*path)
	if err != nil {
		fmt.Printf("Path '%s' does not exist\n", *path)
		return
	}

	d := dl.New(*url, *path, *name, *c)
	n, err := d.Size()
	if err != nil {
		log.Fatal(err)
	}

	var divBy float64
	sizeIn := "bytes"
	if n > 1024*1024*1024 {
		sizeIn = "GB"
		divBy = 1024 * 1024 * 1024
	} else if n > 1024*1024 {
		sizeIn = "MB"
		divBy = 1024 * 1024
	} else if n > 1024 {
		sizeIn = "KB"
		divBy = 1024
	}

	fs := float64(n) / divBy
	fmt.Printf("file size: %.2f %v\n", fs, sizeIn)

	ch := make(chan int64)
	go func() {
		fsb := float64(n) // file size in bytes
		for {
			tot := <-ch
			fmt.Printf("\r%.2f/%.2f %v | %.2f%% complete", float64(tot)/divBy, fs, sizeIn, float64(tot)/fsb*100)
		}
	}()

	err = d.Do(ch)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("\nDownload complete in %v\n", d.Duration())
}
