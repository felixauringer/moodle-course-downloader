package main

import (
	"errors"
	"flag"
	"fmt"
	set "github.com/deckarep/golang-set/v2"
	"github.com/joho/godotenv"
	"golang.org/x/net/html"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
)

type MoodleResource struct {
	url.URL
}

func NewResource(resourceUrl *url.URL) MoodleResource {
	// only Scheme, Host, Path and the id parameter from RawQuery are relevant
	resource := MoodleResource{
		url.URL{
			Scheme: resourceUrl.Scheme,
			Host:   resourceUrl.Host,
			Path:   resourceUrl.Path,
		},
	}
	values, err := url.ParseQuery(resourceUrl.RawQuery)
	if err != nil {
		log.Fatal(err)
	}
	if idValue := values.Get("id"); idValue != "" {
		newValues := url.Values{}
		newValues.Set("id", idValue)
		resource.URL.RawQuery = newValues.Encode()
	}
	return resource
}

func (mr MoodleResource) IsRelevant() bool {
	for _, prefix := range []string{"/user", "/mod/forum", "/theme", "/course/search.php", "/my", "/message", "/auth", "/login", "/portfolio", "/course/user.php", "/grade/report/overview"} {
		if strings.HasPrefix(mr.URL.Path, prefix) {
			return false
		}
	}
	if mr.URL.Path == "" || mr.URL.Path == "/" {
		return false
	}
	return true
}

type Crawler struct {
	*http.Client
	Base       *url.URL
	CourseId   int
	Done       set.Set[MoodleResource]
	DoneMutex  *sync.Mutex
	Queue      set.Set[MoodleResource]
	QueueMutex *sync.Mutex
}

func NewCrawler(base *url.URL, courseId int) (*Crawler, error) {
	if err := godotenv.Load(); err != nil {
		return nil, fmt.Errorf("could not load .env: %w", err)
	}
	sessionCookie := &http.Cookie{
		Name:  os.Getenv("COOKIE_NAME"),
		Value: os.Getenv("COOKIE_VALUE"),
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("could not create cookie jar: %w", err)
	}
	jar.SetCookies(base, []*http.Cookie{sessionCookie})
	client := &http.Client{Jar: jar}
	return &Crawler{
		client,
		base,
		courseId,
		set.NewSet[MoodleResource](),
		&sync.Mutex{},
		set.NewSet[MoodleResource](),
		&sync.Mutex{},
	}, nil
}

func (c *Crawler) startPoint() string {
	parameters := url.Values{}
	parameters.Set("id", strconv.Itoa(c.CourseId))
	target := *c.Base
	target.Path += "/course/view.php"
	target.RawQuery = parameters.Encode()
	return target.String()
}

func (c *Crawler) isExternal(resource MoodleResource) bool {
	return resource.Host != c.Base.Host
}

func (c *Crawler) Run() {
	c.enqueue(c.startPoint(), MoodleResource{})
	for {
		c.QueueMutex.Lock()
		// TODO: with multiple threads, the queue could only be empty temporarily
		if c.Queue.Cardinality() == 0 {
			c.QueueMutex.Unlock()
			break
		}
		element, ok := c.Queue.Pop()
		if !ok {
			log.Fatal("could not pop next element from queue")
		}
		c.DoneMutex.Lock()
		c.Done.Add(element)
		c.DoneMutex.Unlock()
		c.QueueMutex.Unlock()
		c.fetchPage(element)
	}
}

func (c *Crawler) enqueue(targetUrl string, reference MoodleResource) {
	newTarget, err := url.Parse(targetUrl)
	if err != nil {
		log.Fatal(err)
	}
	if newTarget.Scheme == "http" || newTarget.Scheme == "https" {
		if !newTarget.IsAbs() {
			newTarget = reference.ResolveReference(newTarget)
		}
		resource := NewResource(newTarget)
		if c.isExternal(resource) {
			c.DoneMutex.Lock()
			c.Done.Add(resource)
			c.DoneMutex.Unlock()
		} else if resource.IsRelevant() {
			c.QueueMutex.Lock()
			c.DoneMutex.Lock()
			if !c.Queue.Contains(resource) && !c.Done.Contains(resource) {
				c.Queue.Add(resource)
				log.Printf("enqueued %q", resource.String())
			}
			c.DoneMutex.Unlock()
			c.QueueMutex.Unlock()
		}
	} else if newTarget.Scheme == "mailto" {
		log.Printf("Found mail address: %+v", newTarget)
	}
}

func (c *Crawler) parseHtml(body io.ReadCloser, resource MoodleResource) {
	document, err := html.Parse(body)
	if err != nil {
		log.Fatal(err)
	}
	var findBodyAndContent func(*html.Node) (*html.Node, *html.Node)
	findBodyAndContent = func(current *html.Node) (*html.Node, *html.Node) {
		if current.Type == html.ElementNode && current.Data == "div" {
			for _, attribute := range current.Attr {
				if attribute.Key == "class" && attribute.Val == "region-main" {
					return nil, current
				}
			}
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			bodyNode, contentNode := findBodyAndContent(child)
			if bodyNode != nil && contentNode != nil {
				return bodyNode, contentNode
			} else if contentNode != nil {
				if current.Type == html.ElementNode && current.Data == "body" {
					return current, contentNode
				} else {
					return nil, contentNode
				}
			}
		}
		return nil, nil
	}
	bodyNode, contentNode := findBodyAndContent(document)
	if body != nil && contentNode != nil {
		contentNode.NextSibling = nil
		contentNode.PrevSibling = nil
		contentNode.Parent = bodyNode
		bodyNode.FirstChild = contentNode
		bodyNode.LastChild = contentNode
	} else {
		log.Printf("found html without expected structure: %q", resource.String())
	}

	c.extractLinks(document, resource)
	//if err := html.Render(os.Stdout, document); err != nil {
	//	log.Fatal(err)
	//}
}

func (c *Crawler) fetchPage(resource MoodleResource) {
	log.Printf("fetching %q", resource.String())
	response, err := c.Client.Get(resource.URL.String())
	if err != nil {
		log.Fatal(err)
	}
	defer func(Body io.ReadCloser) {
		if err := Body.Close(); err != nil {
			log.Fatal(err)
		}
	}(response.Body)

	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		contentTypeHeader := response.Header.Get("content-type")
		contentType := strings.Split(contentTypeHeader, ";")[0]
		switch contentType {
		case "text/html":
			c.parseHtml(response.Body, resource)
		default:
			// TODO: save file
		}
	case response.StatusCode >= 300 && response.StatusCode < 400:
		location := response.Header.Get("location")
		log.Println("redirect to", location)
		c.enqueue(location, resource)
	default:
		log.Printf("bad response (%d) for %q", response.StatusCode, resource.String())
	}
}

func (c *Crawler) extractLinks(node *html.Node, reference MoodleResource) {
	if node.Type == html.ElementNode {
		var attributeName string
		if node.Data == "a" {
			attributeName = "href"
		} else if node.Data == "img" {
			attributeName = "src"
		} else {
			// TODO: are there other nodes that should be downloaded?
		}
		if attributeName != "" {
			for _, attribute := range node.Attr {
				if attribute.Key == attributeName {
					c.enqueue(attribute.Val, reference)
					break
				}
			}
		}
	}
	for current := node.FirstChild; current != nil; current = current.NextSibling {
		c.extractLinks(current, reference)
	}
}

func loadConfiguration() (*url.URL, int, error) {
	var courseId int
	var domain string
	var prefix string
	flag.IntVar(&courseId, "id", 0, "the ID of the moodle course")
	flag.StringVar(&domain, "domain", "", "the domain, e.g. `hpi.de`")
	flag.StringVar(&prefix, "prefix", "", "optional path prefix, e.g. `/moodle`")
	flag.Parse()
	parsedUrl, err := url.Parse(fmt.Sprintf("https://%s%s", domain, prefix))
	if err != nil {
		return nil, 0, fmt.Errorf("could not parse URL from config: %w", err)
	}
	if parsedUrl.Host == "" || courseId == 0 {
		return nil, 0, errors.New("host and course ID have to be specified")
	}
	return parsedUrl, courseId, nil
}

func main() {
	base, courseId, err := loadConfiguration()
	if err != nil {
		log.Fatal(err)
	}
	crawler, err := NewCrawler(base, courseId)
	if err != nil {
		log.Fatal(err)
	}
	crawler.Run()
}
