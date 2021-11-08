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
	"time"
)

type Dl struct {
	Url      string // source URL to download from
	Path     string // the directory in which the downloaded file is to be stored
	Name     string // name to be given to the file while saving
	C        int    // number of chunks/parts in which the file is to be downloaded
	fileSize int
}

type dlTask struct {
	c [2]int // chunk start and stop bytes
	i int    // chunk index
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
	d := Dl{
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
	fmt.Printf("\nDownload completed in %s seconds. File saved to %s\n", time.Since(startTime).String(), *path)
}

// Do downloads the file
func (d Dl) Do() error {
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

	d.fileSize = fileSize
	fmt.Println(fileSize)

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

	// make and send download tasks
	dch := make(chan *dlTask, d.C) // to send download tasks
	for i, c := range chunks {
		dch <- &dlTask{c, i}
	}

	err = d.dl(dch)
	if err != nil {
		d.delete()
		return err
	}

	return d.merge()
}

func (d Dl) dl(dch chan *dlTask) error {
	addch := make(chan int64)
	decch := make(chan int64)
	go d.printProgress(addch, decch)
	errCh := make(chan error)
	errMap := make(map[int]int)
	sC := 0
	go func() {
		for dlt := range dch {
			go func(dlt *dlTask) {
				tot, err := d.dlchunk(addch, dlt.i, dlt.c)
				if err != nil {
					errMap[dlt.i] += 1
					if errMap[dlt.i] > 1200 {
						errCh <- err
					} else {
						decch <- tot
						<-time.After(time.Second)
						dch <- dlt
					}
				} else {
					sC += 1
					if sC == d.C {
						errCh <- nil
					}
				}
			}(dlt)
		}
	}()
	return <-errCh
}

// downloadchunk downloads a single chunk and saves content to a tmp file
func (d Dl) dlchunk(ch chan<- int64, i int, c [2]int) (int64, error) {
	r, err := d.getNewRequest("GET")
	if err != nil {
		return 0, err
	}
	r.Header.Set("Range", fmt.Sprintf("bytes=%v-%v", c[0], c[1]))
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode > 299 {
		return 0, fmt.Errorf("can't process; response is %v", resp.StatusCode)
	}

	f, err := os.OpenFile(fmt.Sprintf("%v-chunk-%v.tmp", d.Name, i), os.O_CREATE|os.O_WRONLY, 07777)
	if err != nil {
		return 0, fmt.Errorf("here")
	}
	defer f.Close()

	var rerr error
	var n int64
	var tot int64
	for rerr == nil {
		n, rerr = io.CopyN(f, resp.Body, 1024*256)
		ch <- n
		tot += n
	}
	if rerr != io.EOF {
		return tot, fmt.Errorf("here -- %v", err)
	}
	return tot, nil
}

// getNewRequest returns a new http request with given method
func (d Dl) getNewRequest(method string) (*http.Request, error) {
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

func (d Dl) printProgress(addch, decch <-chan int64) {
	go func() {
		var tot, divBy float64
		fs := float64(d.fileSize)
		fsb := float64(d.fileSize)
		sizeIn := "bytes"
		if d.fileSize > 1024*1024*1024 {
			sizeIn = "GB"
			divBy = 1024 * 1024 * 1024
		} else if d.fileSize > 1024*1024 {
			sizeIn = "MB"
			divBy = 1024 * 1024
		} else if d.fileSize > 1024 {
			sizeIn = "KB"
			divBy = 1024
		}

		fs = fs / divBy
		fmt.Printf("file size: %.2f %v\n", fs, sizeIn)

		for {
			select {
			case add := <-addch:
				tot += float64(add)
			case sub := <-decch:
				tot -= float64(sub)
			default:
				fmt.Printf("\r%.2f/%.2f %v | %.2f%% complete", tot/divBy, fs, sizeIn, tot/fsb*100)
			}
		}
	}()
}

// merge merges tmp files to single file and delete tmp files
func (d Dl) merge() error {
	f, err := os.OpenFile(d.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, os.ModePerm)
	if err != nil {
		return err
	}
	defer f.Close()

	for i := 0; i < d.C; i++ {
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

// merge merges tmp files to single file and delete tmp files
func (d Dl) delete() {
	for i := 0; i < d.C; i++ {
		os.Remove(fmt.Sprintf("%v-chunk-%v.tmp", d.Name, i))
	}
}
