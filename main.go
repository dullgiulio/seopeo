package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	nurl "net/url"
	"path"

	"golang.org/x/net/html"
)

var (
	bodyTag  = []byte("body")
	hrefAttr = []byte("href")
)

type pfn func() (pfn, error)

type page struct {
	r    io.Reader
	url  *nurl.URL
	tok  *html.Tokenizer
	urls []string
}

func newPage(r io.Reader, url *nurl.URL) *page {
	return &page{
		r:    r,
		url:  url,
		tok:  html.NewTokenizer(r),
		urls: make([]string, 0),
	}
}

func (p *page) normalize(surl string) (string, error) {
	url, err := nurl.Parse(surl)
	if err != nil {
		return "", err
	}
	// Ignore links to other domains
	// TODO: be more lax about 80 and 443 with right scheme
	if url.Host != "" && url.Host != p.url.Host {
		return "", nil
	}
	url.Host = p.url.Host
	if url.Scheme != "" {
		// Skip unhandled schemes
		if url.Scheme != "http" || url.Scheme != "https" {
			return "", nil
		}
		if url.Scheme != p.url.Scheme {
			return "", fmt.Errorf("schema is %s, it was %s", url.Scheme, p.url.Scheme)
		}
	}
	url.Scheme = p.url.Scheme
	// Opaque: ignored
	// User: ignored
	if url.Path[0] != '/' {
		url.Path = p.url.Path + url.Path
	}
	url.Path = path.Clean(url.Path)
	if url.Path == "/" {
		url.Path = ""
	}
	url.Fragment = ""
	surl = url.String()
	// Skip internal link, only fragment
	if surl[0] == '#' {
		return "", nil
	}
	return surl, nil
}

func (p *page) findBody() (pfn, error) {
	for {
		tt := p.tok.Next()
		if tt == html.ErrorToken {
			break
		}
		if tt != html.StartTagToken {
			continue
		}
		tn, _ := p.tok.TagName()
		if bytes.Compare(tn, bodyTag) == 0 {
			return p.findAnchor, nil
		}
	}
	err := p.tok.Err()
	if err == io.EOF {
		return nil, errors.New("body not found")
	}
	return nil, err
}

func (p *page) findAnchor() (pfn, error) {
	for {
		tt := p.tok.Next()
		if tt == html.ErrorToken {
			break
		}
		if tt != html.StartTagToken {
			continue
		}
		tn, hasAttrs := p.tok.TagName()
		if len(tn) != 1 || tn[0] != 'a' || !hasAttrs {
			continue
		}
		var (
			key, val []byte
			more     bool = true
		)
		for more {
			key, val, more = p.tok.TagAttr()
			if bytes.Compare(key, hrefAttr) != 0 {
				continue
			}
			ourl := string(val)
			url, err := p.normalize(ourl)
			if err != nil {
				log.Printf("html parser: cannot handle link %s: %s", ourl, err)
				continue
			}
			if url != "" {
				p.urls = append(p.urls, url)
			}
		}
	}
	err := p.tok.Err()
	if err == io.EOF {
		return nil, nil
	}
	return nil, err
}

func (p *page) parse() error {
	f := p.findBody
	for {
		var err error
		f, err = f()
		if err != nil {
			return fmt.Errorf("cannot parse HTML: %s", err)
		}
		if f == nil {
			break
		}
	}
	return nil
}

func httpBodyReader(url string) (io.Reader, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("cannot GET from HTTP: %s", err)
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cannot read from HTTP: %s", err)
	}
	return bytes.NewReader(body), nil
}

func newWorkers(n int, c *crawler) chan<- string {
	ch := make(chan string, n)
	for i := 0; i < n; i++ {
		go worker(ch, c)
	}
	return ch
}

func worker(ch <-chan string, c *crawler) {
	for url := range ch {
		r, err := httpBodyReader(url)
		if err != nil {
			log.Printf("worker error: http: %s", err)
			c.done(nil)
			continue
		}
		p := newPage(r, c.baseurl)
		if err := p.parse(); err != nil {
			log.Printf("worker error: parser: %s", err)
			c.done(nil)
			continue
		}
		c.done(p.urls)
	}
}

type crawler struct {
	urls     map[string]bool
	fn       chan func() error
	fin      chan struct{}
	workers  chan<- string
	baseurl  *nurl.URL
	nworkers int
	nbusy    int
	base     string
	hasWork  bool
}

func newCrawler(base string, nworkers int) (*crawler, error) {
	burl, err := nurl.Parse(base)
	if err != nil {
		return nil, err
	}
	c := &crawler{
		base:     base,
		nworkers: nworkers,
		baseurl:  burl,
		urls:     make(map[string]bool),
		fn:       make(chan func() error),
		fin:      make(chan struct{}),
	}
	c.workers = newWorkers(nworkers, c)
	c.urls[base] = false
	go c.run()
	c.fn <- c.sched
	return c, nil
}

func (c *crawler) wait() {
	<-c.fin
}

func (c *crawler) sched() error {
	var hasWork bool
	for url, done := range c.urls {
		if done {
			continue
		}
		hasWork = true
		c.urls[url] = true
		c.nbusy++
		c.workers <- url
		if c.nbusy >= c.nworkers {
			break
		}
	}
	c.hasWork = hasWork
	return nil
}

func (c *crawler) done(urls []string) {
	c.fn <- func() error {
		c.nbusy--
		for _, url := range urls {
			if _, ok := c.urls[url]; !ok {
				c.urls[url] = false
				c.hasWork = true
			}
		}
		return nil
	}
}

func (c *crawler) run() {
	for fn := range c.fn {
		if err := fn(); err != nil {
			log.Printf("crawler error: %s", err)
		}
		// No more work and no results to wait for, exit.
		if !c.hasWork && c.nbusy == 0 {
			break
		}
		if c.hasWork {
			c.sched()
		}
	}
	close(c.workers)
	close(c.fin)
}

func main() {
	// TODO: as real flag
	nworkers := 4
	flag.Parse()
	c, err := newCrawler(flag.Arg(0), nworkers)
	if err != nil {
		log.Fatalf("cannot start crawler: %s", err)
	}
	c.wait()
	for url := range c.urls {
		fmt.Printf("%s\n", url)
	}
}
