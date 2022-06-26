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
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type MoodleResource struct {
	url.URL
}

func NewResource(resourceUrl *url.URL) MoodleResource {
	// only Scheme, Host, Path and some parameters from RawQuery are relevant
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
	newValues := url.Values{}
	for _, key := range []string{"id"} {
		if value := values.Get(key); value != "" {
			newValues.Set(key, value)
		}
	}
	resource.URL.RawQuery = newValues.Encode()
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
	BasePath   string
	BaseURL    *url.URL
	CourseId   int
	Done       set.Set[MoodleResource]
	DoneMutex  *sync.Mutex
	Queue      set.Set[MoodleResource]
	QueueMutex *sync.Mutex
}

func NewCrawler(baseURL *url.URL, courseId int, basePath string) (*Crawler, error) {
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
	jar.SetCookies(baseURL, []*http.Cookie{sessionCookie})
	client := &http.Client{Jar: jar}
	return &Crawler{
		client,
		filepath.Join(basePath, fmt.Sprintf("course-%d", courseId)),
		baseURL,
		courseId,
		set.NewThreadUnsafeSet[MoodleResource](),
		&sync.Mutex{},
		set.NewThreadUnsafeSet[MoodleResource](),
		&sync.Mutex{},
	}, nil
}

func (c *Crawler) startPoint() string {
	parameters := url.Values{}
	parameters.Set("id", strconv.Itoa(c.CourseId))
	target := *c.BaseURL
	target.Path += "/course/view.php"
	target.RawQuery = parameters.Encode()
	return target.String()
}

func (c *Crawler) isExternal(resource MoodleResource) bool {
	return resource.Host != c.BaseURL.Host
}

func (c *Crawler) filePath(resource MoodleResource, contentType string) string {
	components := []string{c.BasePath, fmt.Sprintf("%s-%s", resource.Scheme, resource.Host), resource.Path}
	values, err := url.ParseQuery(resource.RawQuery)
	if err == nil {
		idParameter := values.Get("id")
		if idParameter != "" {
			components = append(components, fmt.Sprintf("id-%s", idParameter))
		}
	}
	filePath := filepath.Join(components...)
	if filepath.Ext(filePath) == "" {
		var extension string
		switch contentType {
		case "text/html":
			extension = "html"
		case "application/pdf":
			extension = "pdf"
		default:
			extension = "bin"
		}
		filePath = fmt.Sprintf("%s.%s", filePath, extension)
	}
	dirPath := filepath.Dir(filePath)
	if err := os.MkdirAll(dirPath, 0755); err != nil {
		log.Fatal(err)
	}
	return filePath
}

func (c *Crawler) exportSummary() {
	components := []string{c.BasePath, fmt.Sprintf("%s-%s", c.BaseURL.Scheme, c.BaseURL.Host), "summary.txt"}
	filePath := filepath.Join(components...)
	outputFile, err := os.Create(filePath)
	if err != nil {
		log.Fatal(err)
	}
	defer func(outputFile *os.File) {
		err := outputFile.Close()
		if err != nil {
			log.Fatal(err)
		}
	}(outputFile)
	downloaded := make([]string, 0)
	external := make([]string, 0)
	c.DoneMutex.Lock()
	for resource := range c.Done.Iterator().C {
		if c.isExternal(resource) {
			external = append(external, resource.String())
		} else {
			downloaded = append(downloaded, resource.String())
		}
	}
	c.DoneMutex.Unlock()
	sort.Strings(downloaded)
	sort.Strings(external)
	_, err = outputFile.WriteString(fmt.Sprintf("The crawler downloaded %d moodle resources:\n", len(downloaded)))
	if err != nil {
		log.Fatal(err)
	}
	for _, downloadedURL := range downloaded {
		_, err = outputFile.WriteString(fmt.Sprintf("\t%s\n", downloadedURL))
		if err != nil {
			log.Fatal(err)
		}
	}
	_, err = outputFile.WriteString(fmt.Sprintf("The crawler found %d external resources:\n", len(external)))
	if err != nil {
		log.Fatal(err)
	}
	for _, externalURL := range external {
		_, err = outputFile.WriteString(fmt.Sprintf("\t%s\n", externalURL))
		if err != nil {
			log.Fatal(err)
		}
	}
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
	c.exportSummary()
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

func (c *Crawler) saveHTML(body io.Reader, resource MoodleResource, file io.Writer) {
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
	if err := html.Render(file, document); err != nil {
		log.Fatal(err)
	}
}

func (c *Crawler) saveArbitraryFile(body io.Reader, file io.Writer) {
	_, err := io.Copy(file, body)
	if err != nil {
		log.Fatal(err)
	}
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
		outputFile, err := os.Create(c.filePath(resource, contentType))
		if err != nil {
			log.Fatal(err)
		}
		defer func(outputFile *os.File) {
			err := outputFile.Close()
			if err != nil {
				log.Fatal(err)
			}
		}(outputFile)

		switch contentType {
		case "text/html":
			c.saveHTML(response.Body, resource, outputFile)
		default:
			c.saveArbitraryFile(response.Body, outputFile)
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

func loadConfiguration() (*url.URL, int, string, error) {
	var courseId int
	var domain string
	var uncleanedPath string
	flag.IntVar(&courseId, "id", 0, "the ID of the moodle course")
	flag.StringVar(&domain, "domain", "", "the domain, e.g. `hpi.de`")
	flag.StringVar(&uncleanedPath, "dir", "./output", "absolute or relative output directory")
	flag.Parse()
	if domain == "" || courseId == 0 {
		return nil, 0, "", errors.New("host and course ID have to be specified")
	}
	baseUrl := &url.URL{Scheme: "https", Host: domain}
	cleanedPath, err := filepath.Abs(uncleanedPath)
	if err != nil {
		return nil, 0, "", fmt.Errorf("could not deduce absolute path: %w", err)
	}
	return baseUrl, courseId, cleanedPath, nil
}

func main() {
	base, courseId, cleanedPath, err := loadConfiguration()
	if err != nil {
		log.Fatal(err)
	}
	crawler, err := NewCrawler(base, courseId, cleanedPath)
	if err != nil {
		log.Fatal(err)
	}
	crawler.Run()
}
