package main

import (
	"flag"
	"fmt"
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

func (mr MoodleResource) IsExternal() bool {
	return mr.Host != configuration.BaseUrl.Host
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

func (c config) StartResource() MoodleResource {
	parameters := url.Values{}
	parameters.Set("id", strconv.Itoa(c.CourseId))
	target := *configuration.BaseUrl
	target.Path += "/course/view.php"
	target.RawQuery = parameters.Encode()
	return MoodleResource{&target}
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
	configuration = parseFlags()
	if err := godotenv.Load(); err != nil {
		log.Fatal(err)
	}
	sessionCookie := &http.Cookie{
		Name:  os.Getenv("COOKIE_NAME"),
		Value: os.Getenv("COOKIE_VALUE"),
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		log.Fatal(err)
	}
	jar.SetCookies(configuration.BaseUrl, []*http.Cookie{sessionCookie})
	client = &http.Client{Jar: jar}
}

func parseHtml(resource MoodleResource, body io.ReadCloser) {
	document, err := html.Parse(body)
	if err != nil {
		log.Fatal(err)
	}
	if resource.IsCourse() {
		var findBodyAndContent func(*html.Node) (*html.Node, *html.Node)
		findBodyAndContent = func(current *html.Node) (*html.Node, *html.Node) {
			if current.Type == html.ElementNode && current.Data == "div" {
				for _, attribute := range current.Attr {
					if attribute.Key == "class" && attribute.Val == "course-content" {
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
		if body == nil || contentNode == nil {
			log.Fatal("course page without course content")
		}
		contentNode.NextSibling = nil
		contentNode.PrevSibling = nil
		contentNode.Parent = bodyNode
		bodyNode.FirstChild = contentNode
		bodyNode.LastChild = contentNode
	}
	//links := extractLinks(document, resource.URL)
	//for _, link := range links {
	//	fmt.Println(*link)
	//}
	if err := html.Render(os.Stdout, document); err != nil {
		log.Fatal(err)
	}
}

func fetchPage(resource MoodleResource) {
	response, err := client.Get(resource.URL.String())
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
			parseHtml(resource, response.Body)
		default:
			// TODO: save file
		}
	case response.StatusCode >= 300 && response.StatusCode < 400:
		location := response.Header.Get("location")
		newTarget, err := url.Parse(location)
		if err != nil {
			log.Fatal(err)
		}
		log.Println("redirect to", newTarget)
		fetchPage(MoodleResource{newTarget})
	default:
		log.Fatal("bad response", response.StatusCode, resource.URL)
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
					if href.Scheme == "javascript" {
						break
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
	fetchPage(configuration.StartResource())
}
