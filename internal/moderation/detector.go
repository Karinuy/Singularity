package moderation

import (
	"fmt"
	"regexp"
	"strings"
)

var linkPattern = regexp.MustCompile(`(?i)(https?://|www\.|t\.me/|telegram\.me/|@[a-z0-9_]{5,})`)

type Result struct {
	Advertisement bool
	Reason        string
	Score         int
}

func DetectAdvertisement(text string, keywords []string, threshold int, linkLimit int) Result {
	normalized := strings.ToLower(strings.TrimSpace(text))
	if normalized == "" {
		return Result{}
	}

	score := 0
	matched := make([]string, 0, 3)
	for _, keyword := range keywords {
		keyword = strings.ToLower(strings.TrimSpace(keyword))
		if keyword == "" {
			continue
		}
		if strings.Contains(normalized, keyword) {
			score += 2
			if len(matched) < 3 {
				matched = append(matched, keyword)
			}
		}
	}

	linkCount := len(linkPattern.FindAllString(normalized, -1))
	if linkCount >= linkLimit {
		score += 2
	} else if linkCount == 1 {
		score++
	}

	if strings.Count(normalized, "\n") >= 5 && linkCount > 0 {
		score++
	}

	if score < threshold {
		return Result{Score: score}
	}

	reason := "advertisement score threshold reached"
	if len(matched) > 0 {
		reason = fmt.Sprintf("matched keywords: %s", strings.Join(matched, ", "))
	} else if linkCount > 0 {
		reason = fmt.Sprintf("link count: %d", linkCount)
	}

	return Result{
		Advertisement: true,
		Reason:        reason,
		Score:         score,
	}
}
