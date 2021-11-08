package main

import (
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Download struct {
	Url  string // source URL to download from
	Path string // the directory in which the downloaded file is to be stored
	Name string // name to be given to the file while saving
	C    int    // number of chunks/parts in which the file is to be downloaded
}

func main() {
	url := flag.String("url", "", "URL to download from")
	path := flag.String("path", ".", "path in which the downloaded file is to be saved")
	name := flag.String("name", "", "name to save the file with")
	c := flag.Int("chunks", 10, "number of chunks in which the file is to be downloaded")
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

	if *name == "" {
		s := strings.Split(*url, "/")
		*name = s[len(s)-1]
	}

	*path = filepath.Join(*path, *name)

	startTime := time.Now()
	d := Download{
		Url:  *url,
		Path: *path,
		Name: *name,
		C:    *c,
	}

	err = d.Do()
	if err != nil {
		fmt.Printf("Failed to download: %s\n", err)
		return
	}
	fmt.Printf("\nDownload completed in %.2f seconds. File saved to %s\n", time.Since(startTime).Seconds(), *path)
}

// Do downloads the file
func (d Download) Do() error {
	// make a HEAD request to get the content-length
	r, err := d.getNewRequest("HEAD")
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		return err
	}
	if resp.StatusCode > 299 {
		return fmt.Errorf("response from source is %v", resp.StatusCode)
	}

	fileSize, err := strconv.Atoi(resp.Header.Get("Content-Length"))
	if err != nil {
		return fmt.Errorf("could not determine file size")
	}

	// chunks is a slice in the format [[0, 1000], [1001, 2000], ...] (example)
	// representing the start and ending byte of each chunk that is to be downloaded
	// by each goroutine
	var chunks = make([][2]int, d.C)
	chunkSize := fileSize / d.C

	for i := range chunks {
		if i == 0 {
			chunks[i][0] = 0
			chunks[i][1] = chunkSize
		} else {
			chunks[i][0] = chunks[i-1][1] + 1
			chunks[i][1] = chunks[i][0] + chunkSize
		}
	}

	ch := make(chan int64)
	var wg sync.WaitGroup
	for i, s := range chunks {
		wg.Add(1)
		go func(i int, s [2]int) {
			defer wg.Done()
			err = d.downloadchunk(ch, i, s)
			if err != nil {
				fmt.Printf("error :: chunk %d: %v\n", i, err)
				return
			}
		}(i, s)
	}

	go func() {
		// to print the percentage of download completed
		// ---
		// tot - total bytes downloaded
		var tot, divBy int64
		sizeIn := "bytes"
		if fileSize > 1024*1024*1024 {
			sizeIn = "GB"
			divBy = 1024 * 1024 * 1024
		} else if fileSize > 1024*1024 {
			sizeIn = "MB"
			divBy = 1024 * 1024
		} else if fileSize > 1024 {
			sizeIn = "KB"
			divBy = 1024
		}

		fmt.Printf("file size: %v %v\n", fileSize/int(divBy), sizeIn)
		fmt.Println("starting download...")

		for {
			tot += <-ch
			fmt.Printf("\r%v/%v %v | %.1f%% completed", tot/divBy, int64(fileSize)/divBy, sizeIn, float64(tot)/float64(fileSize)*100)
		}
	}()

	wg.Wait()
	return d.merge(chunks)
}

// downloadchunk downloads a single chunk and saves content to a tmp file
func (d Download) downloadchunk(ch chan<- int64, i int, c [2]int) error {
	r, err := d.getNewRequest("GET")
	if err != nil {
		return err
	}
	r.Header.Set("Range", fmt.Sprintf("bytes=%v-%v", c[0], c[1]))
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		return err
	}
	if resp.StatusCode > 299 {
		return fmt.Errorf("can't process; response is %v", resp.StatusCode)
	}

	f, _ := os.OpenFile(fmt.Sprintf("%v-chunk-%v.tmp", d.Name, i), os.O_CREATE|os.O_WRONLY, 07777)
	defer f.Close()

	var rerr error
	var n int64
	for rerr == nil {
		n, rerr = io.CopyN(f, resp.Body, 1024*1024)
		ch <- n
	}
	if rerr != io.EOF {
		return rerr
	}
	return nil
}

// getNewRequest returns a new http request with given method
func (d Download) getNewRequest(method string) (*http.Request, error) {
	r, err := http.NewRequest(
		method,
		d.Url,
		nil,
	)
	if err != nil {
		return nil, err
	}
	r.Header.Set("User-Agent", "downloader")
	return r, nil
}

// merge merges tmp files to single file and delete tmp files
func (d Download) merge(chunks [][2]int) error {
	f, err := os.OpenFile(d.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, os.ModePerm)
	if err != nil {
		return err
	}
	defer f.Close()

	for i := range chunks {
		tmpFileName := fmt.Sprintf("%v-chunk-%v.tmp", d.Name, i)
		b, err := ioutil.ReadFile(tmpFileName)
		if err != nil {
			return err
		}
		_, err = f.Write(b)
		if err != nil {
			return err
		}
		err = os.Remove(tmpFileName)
		if err != nil {
			return err
		}
	}
	return nil
}
