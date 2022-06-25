package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
)

type config struct {
	BaseUrl  *url.URL
	CourseId int
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
	buffer := make([]byte, 512)
	for {
		_, err := response.Body.Read(buffer)
		if err != nil {
			break
		} else {
			fmt.Print(string(buffer))
		}
	}
	err = response.Body.Close()
	if err != nil {
		log.Fatal(err)
	}
}

func main() {
	initialize()
	fetchPage(configuration.StartUrl())
}
