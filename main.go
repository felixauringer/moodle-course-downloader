package main

import (
	"flag"
	"fmt"
	"log"
	"net/url"
)

type config struct {
	BaseUrl  *url.URL
	CourseId int
}

func parseFlags() *config {
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
	return &config{parsedUrl, courseId}
}

func main() {
	configuration := parseFlags()
	fmt.Printf("%+v\n", *configuration.BaseUrl)
}
