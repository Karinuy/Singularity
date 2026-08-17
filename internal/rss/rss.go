package rss

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const maxFeedBytes = 5 << 20

type Feed struct {
	Title string
	Items []Item
}

type Item struct {
	Title       string
	Link        string
	Key         string
	PublishedAt *time.Time
}

func Fetch(ctx context.Context, feedURL string, timeout time.Duration) (Feed, error) {
	parsed, err := url.Parse(feedURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return Feed{}, errors.New("feed url must be http or https")
	}

	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return Feed{}, err
	}
	req.Header.Set("User-Agent", "SingularityBot/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return Feed{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Feed{}, fmt.Errorf("feed returned status %s", resp.Status)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFeedBytes))
	if err != nil {
		return Feed{}, err
	}

	feed, err := parseRSS(data)
	if err == nil && len(feed.Items) > 0 {
		return feed, nil
	}
	feed, err = parseAtom(data)
	if err == nil && len(feed.Items) > 0 {
		return feed, nil
	}
	return Feed{}, errors.New("unsupported or empty feed")
}

type rssDocument struct {
	XMLName xml.Name `xml:"rss"`
	Channel struct {
		Title string    `xml:"title"`
		Items []rssItem `xml:"item"`
	} `xml:"channel"`
}

type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	GUID        string `xml:"guid"`
	PubDate     string `xml:"pubDate"`
	Description string `xml:"description"`
}

func parseRSS(data []byte) (Feed, error) {
	var doc rssDocument
	if err := xml.Unmarshal(data, &doc); err != nil {
		return Feed{}, err
	}
	if len(doc.Channel.Items) == 0 {
		return Feed{}, errors.New("no rss items")
	}

	items := make([]Item, 0, len(doc.Channel.Items))
	for _, raw := range doc.Channel.Items {
		published := parseTime(raw.PubDate)
		item := Item{
			Title:       clean(raw.Title),
			Link:        clean(raw.Link),
			Key:         clean(raw.GUID),
			PublishedAt: published,
		}
		if item.Key == "" {
			item.Key = firstNonEmpty(item.Link, item.Title, clean(raw.Description))
		}
		if item.Key != "" {
			items = append(items, item)
		}
	}
	sortItems(items)
	return Feed{Title: clean(doc.Channel.Title), Items: items}, nil
}

type atomDocument struct {
	XMLName xml.Name    `xml:"feed"`
	Title   string      `xml:"title"`
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	ID        string     `xml:"id"`
	Title     string     `xml:"title"`
	Updated   string     `xml:"updated"`
	Published string     `xml:"published"`
	Links     []atomLink `xml:"link"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Text string `xml:",chardata"`
}

func parseAtom(data []byte) (Feed, error) {
	var doc atomDocument
	if err := xml.Unmarshal(data, &doc); err != nil {
		return Feed{}, err
	}
	if len(doc.Entries) == 0 {
		return Feed{}, errors.New("no atom entries")
	}

	items := make([]Item, 0, len(doc.Entries))
	for _, raw := range doc.Entries {
		link := ""
		for _, candidate := range raw.Links {
			if candidate.Rel == "" || candidate.Rel == "alternate" {
				link = firstNonEmpty(candidate.Href, clean(candidate.Text))
				break
			}
		}
		published := parseTime(firstNonEmpty(raw.Published, raw.Updated))
		item := Item{
			Title:       clean(raw.Title),
			Link:        clean(link),
			Key:         clean(firstNonEmpty(raw.ID, link, raw.Title)),
			PublishedAt: published,
		}
		if item.Key != "" {
			items = append(items, item)
		}
	}
	sortItems(items)
	return Feed{Title: clean(doc.Title), Items: items}, nil
}

func parseTime(value string) *time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	formats := []string{
		time.RFC1123Z,
		time.RFC1123,
		time.RFC3339,
		time.RFC3339Nano,
		"Mon, 02 Jan 2006 15:04:05 -0700",
		"2006-01-02 15:04:05",
	}
	for _, format := range formats {
		parsed, err := time.Parse(format, value)
		if err == nil {
			return &parsed
		}
	}
	return nil
}

func sortItems(items []Item) {
	sort.SliceStable(items, func(i int, j int) bool {
		left := items[i].PublishedAt
		right := items[j].PublishedAt
		if left == nil || right == nil {
			return i > j
		}
		return left.Before(*right)
	})
}

func clean(value string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(value), " "))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
