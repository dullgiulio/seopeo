package main

import (
	"os"
	"io"
	"fmt"
	"log"
	"errors"

	"golang.org/x/net/html"
)

type pfn func(tt *html.Tokenizer, v *visitor) (pfn, error)

func findBody(z *html.Tokenizer, v *visitor) (pfn, error) {
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		if tt == html.StartTagToken {
			tn, _ := z.TagName()
			if string(tn) == "body" {
				return findAnchor, nil
			}
		}
	}
	err := z.Err()
	if err == io.EOF {
		return nil, errors.New("body not found")
	}
	return nil, err
}

func findAnchor(z *html.Tokenizer, v *visitor) (pfn, error) {
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		if tt == html.StartTagToken {
			tn, ok := z.TagName()
			if len(tn) == 1 && tn[0] == 'a' && ok {
				for {
					key, val, more := z.TagAttr()
					if string(key) == "href" {
						v.add(string(val))
					}
					if !more {
						break
					}
				}
			}
		}
	}
	err := z.Err()
	if err == io.EOF {
		return nil, nil
	}
	return nil, err
}

type visitor struct {
	urls map[string]struct{}
	fn chan func() error
}

func newVisitor() *visitor {
	v := &visitor{
		urls: make(map[string]struct{}),
		fn: make(chan func() error),
	}
	go v.run()
	return v
}

func (v *visitor) add(s string) {
	// TODO: Discard external or not handled urls
	// TODO: add base to url
	v.fn <- func() error {
		v.urls[s] = struct{}{}
		return nil
	}
}

func (v *visitor) run() {
	for fn := range v.fn {
		if err := fn(); err != nil {
			log.Printf("visitor: %s", err)
		}
	}
}

func main() {
	z := html.NewTokenizer(os.Stdin)
	f := findBody
	for {
		var err error
		f, err = f(z)
		if err != nil {
			log.Fatalf("cannot parse HTML: %s", err)
		}
		if f == nil {
			break
		}
	}
}
