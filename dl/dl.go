package dl

import (
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
	fileSize int64  // size of the file to download in bytes
	fullPath string // fullPath is simply Path + Name
	dltime   string // total time taken to complete download
}

type dlTask struct {
	c [2]int // chunk start and stop bytes
	i int    // chunk index
}

// New returns a handle to download (a) file
func New(url, path, name string, chunks int) *Dl {

	if chunks <= 0 {
		chunks = 1
	}

	if path == "" {
		path = "."
	}

	if name == "" {
		s := strings.Split(url, "/")
		name = s[len(s)-1]
	}

	return &Dl{
		Url:      url,
		Path:     path,
		Name:     name,
		C:        chunks,
		fullPath: filepath.Join(path, name),
	}
}

// Size returns the file size in bytes
func (d *Dl) Size() (int64, error) {
	r, err := d.newRequest(http.MethodHead)
	if err != nil {
		return -1, err
	}

	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		return -1, err
	}
	if resp.StatusCode > 299 {
		return -1, fmt.Errorf("response from source is %v", resp.StatusCode)
	}

	fileSize, err := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	if err != nil {
		return -1, err
	}

	d.fileSize = fileSize
	return fileSize, nil
}

// Do downloads the file
func (d *Dl) Do(ch chan<- int64) error {
	if d.fileSize == 0 {
		s, err := d.Size()
		if err != nil {
			return fmt.Errorf("could not determine file size: %v", err)
		}

		d.fileSize = s
	}

	// chunks is a slice in the format [[0, 1000], [1001, 2000], ...] (example)
	// representing the start and ending byte of each chunk that is to be downloaded
	// by each goroutine
	var chunks = make([][2]int, d.C)
	chunkSize := int(d.fileSize) / d.C

	for i := range chunks {
		if i == 0 {
			chunks[i][0] = 0
			chunks[i][1] = chunkSize
		} else {
			chunks[i][0] = chunks[i-1][1] + 1
			chunks[i][1] = chunks[i][0] + chunkSize
		}
	}

	dch := make(chan *dlTask, d.C)
	// for each chunk, create a new dlTask and send it to the channel dch.
	// Dl.dl will consume the incoming download tasks and give then to
	// Dl.dlchunk method to download particular chunk of bytes.
	for i, c := range chunks {
		dch <- &dlTask{c, i}
	}

	t := time.Now()
	err := d.dl(dch, ch)
	if err != nil {
		// if download was not able to succeed, delete the tmp files generated, if any
		d.delete()
		return err
	}

	// finally, merge the downloaded chunks
	err = d.merge()
	if err != nil {
		return err
	}

	d.dltime = time.Since(t).String()
	return nil
}

func (d *Dl) Duration() string {
	return d.dltime
}

func (d Dl) dl(dch chan *dlTask, ch chan<- int64) error {
	// Dl.dlchunk sends the amount of bytes it downloaded to addCh.
	// The num of bytes received in this chan should be added while showing progress.
	addCh := make(chan int64)
	// Dl.dlchunk sends the amount of bytes it downloaded before failing to decCh.
	// The num of bytes received in this chan should be decremented while showing progress.
	decCh := make(chan int64)
	// errCh is used to receive the error from Dl.dlchunk method
	errCh := make(chan error)
	// errMap holds the number of times error occured while downloading a chunk
	// format: errMap[chunkId]numberOfTimesItFailed
	errMap := make(map[int]int)
	sC := 0 // successCount
	go func() {
		for dlt := range dch {
			go func(dlt *dlTask) {
				tot, err := d.dlchunk(addCh, dlt.i, dlt.c)
				if err != nil {
					errMap[dlt.i] += 1
					if errMap[dlt.i] > 1200 {
						errCh <- err
					} else {
						decCh <- tot
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
	go d.sendProgress(ch, addCh, decCh)
	return <-errCh
}

// dlchunk downloads a single chunk and saves the content to a tmp file
func (d Dl) dlchunk(ch chan<- int64, i int, c [2]int) (int64, error) {
	r, err := d.newRequest(http.MethodGet)
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
		return 0, err
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
		return tot, err
	}
	return tot, nil
}

// sendProgress sends the number of bytes downloaded on channel sendChan.
// It sums the incoming byte counts of addCh and decCh and sends the result to sendCh.
func (d Dl) sendProgress(sendChan chan<- int64, addCh, decCh <-chan int64) {
	var tot int64
	for {
		select {
		case n := <-addCh:
			tot += n
		case n := <-decCh:
			tot -= n
		default:
			sendChan <- tot
		}
	}
}

// merge merges tmp file(s) to single file and then deletes the tmp file(s)
func (d Dl) merge() error {
	f, err := os.OpenFile(d.fullPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, os.ModePerm)
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

// delete deletes tmp file(s)
func (d Dl) delete() {
	for i := 0; i < d.C; i++ {
		os.Remove(fmt.Sprintf("%v-chunk-%v.tmp", d.Name, i))
	}
}

// newRequest creates and returns a new http request of given method type
func (d Dl) newRequest(method string) (*http.Request, error) {
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
