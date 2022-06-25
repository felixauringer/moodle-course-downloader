package main

import (
	"flag"
	"fmt"
	"golang.org/x/net/html"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type config struct {
	BaseUrl  *url.URL
	CourseId int
}

type MoodleResource struct {
	*url.URL
}

func (mr MoodleResource) IsCourse() bool {
	return strings.HasPrefix(mr.URL.Path, "/course")
}

func (mr MoodleResource) Equals(other MoodleResource) bool {
	if mr.Host != other.Host || mr.Scheme != other.Scheme || mr.Path != other.Path {
		return false
	}
	if mr.RawQuery != "" || other.RawQuery != "" {
		theseValues, err := url.ParseQuery(mr.RawQuery)
		if err != nil {
			return false
		}
		otherValues, err := url.ParseQuery(other.RawQuery)
		if err != nil {
			return false
		}
		for _, key := range []string{"id"} {
			if theseValues.Get(key) != otherValues.Get(key) {
				return false
			}
		}
	}
	return true
}

func (c config) StartUrl() *url.URL {
	parameters := url.Values{}
	parameters.Set("id", strconv.Itoa(c.CourseId))
	target := *configuration.BaseUrl
	target.Path += "/course/view.php"
	target.RawQuery = parameters.Encode()
	return &target
}

var client *http.Client
var configuration config

func parseFlags() config {
	var courseId int
	var domain string
	var prefix string
	flag.IntVar(&courseId, "id", 0, "the ID of the moodle course")
	flag.StringVar(&domain, "domain", "", "the domain, e.g. `hpi.de`")
	flag.StringVar(&prefix, "prefix", "", "optional path prefix, e.g. `/moodle`")
	flag.Parse()
	parsedUrl, err := url.Parse(fmt.Sprintf("https://%s%s", domain, prefix))
	if err != nil {
		log.Fatal(err)
	}
	if parsedUrl.Host == "" || courseId == 0 {
		log.Fatal("host and course ID have to be specified")
	}
	return config{parsedUrl, courseId}
}

func initialize() {
	client = &http.Client{}
	configuration = parseFlags()
}

func fetchPage(target *url.URL) {
	response, err := client.Get(target.String())
	if err != nil {
		log.Fatal(err)
	}
	defer func(Body io.ReadCloser) {
		if err := Body.Close(); err != nil {
			log.Fatal(err)
		}
	}(response.Body)

	document, err := html.Parse(response.Body)
	links := extractLinks(document, target)
	for _, link := range links {
		fmt.Println(*link)
	}
}

func extractLinks(node *html.Node, base *url.URL) []*url.URL {
	links := make([]*url.URL, 0)
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
					href, err := url.Parse(attribute.Val)
					if err != nil {
						log.Fatal(err)
					}
					if !href.IsAbs() {
						href = base.ResolveReference(href)
					}
					links = append(links, href)
					break
				}
			}
		}
	}
	for current := node.FirstChild; current != nil; current = current.NextSibling {
		links = append(links, extractLinks(current, base)...)
	}
	return links
}

func main() {
	initialize()
	fetchPage(configuration.StartUrl())
}
