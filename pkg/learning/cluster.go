package learning

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

const (
	defaultClusterWindow        = 7 * 24 * time.Hour
	defaultMinClusterSize       = 3
	defaultMinSharedTopicTokens = 2
)

var clusterStopWords = map[string]struct{}{
	"about": {}, "actually": {}, "after": {}, "again": {}, "agent": {}, "approach": {},
	"better": {}, "between": {}, "error": {}, "errors": {}, "failed": {}, "failure": {},
	"from": {}, "have": {}, "into": {}, "just": {}, "lesson": {}, "lessons": {},
	"main": {}, "need": {}, "please": {}, "same": {}, "session": {}, "should": {},
	"that": {}, "their": {}, "there": {}, "these": {}, "they": {}, "this": {},
	"tool": {}, "tools": {}, "trying": {}, "used": {}, "using": {}, "with": {},
}

type ClusterScanOptions struct {
	Topic                string
	Source               string
	Window               time.Duration
	ReferenceTime        time.Time
	MinClusterSize       int
	MinSharedTopicTokens int
}

type ClusterDryRunResult struct {
	Enabled        bool            `json:"enabled"`
	DisabledReason string          `json:"disabled_reason,omitempty"`
	Window         time.Duration   `json:"window"`
	ScannedLessons int             `json:"scanned_lessons"`
	MatchedLessons int             `json:"matched_lessons"`
	Clusters       []LessonCluster `json:"clusters,omitempty"`
}

type LessonCluster struct {
	Key              string   `json:"key"`
	Topic            string   `json:"topic"`
	Source           string   `json:"source"`
	LessonIDs        []string `json:"lesson_ids"`
	LessonCount      int      `json:"lesson_count"`
	WindowStart      string   `json:"window_start"`
	WindowEnd        string   `json:"window_end"`
	SharedTags       []string `json:"shared_tags,omitempty"`
	SharedTopicTerms []string `json:"shared_topic_terms,omitempty"`
	SourceSignals    []string `json:"source_signals,omitempty"`
}

type clusterLesson struct {
	record LessonRecord
	at     time.Time
	tags   []string
	tokens []string
	sigSet map[string]struct{}
}

func DryRunLessonClusters(cfg *config.LearningConfig, lessons []LessonRecord, opts ClusterScanOptions) ClusterDryRunResult {
	window := opts.Window
	if window <= 0 {
		window = defaultClusterWindow
	}
	result := ClusterDryRunResult{Window: window}
	if cfg == nil || !cfg.GetCrossSessionClusteringEnabled() {
		result.DisabledReason = "cross-session clustering disabled"
		return result
	}
	result.Enabled = true

	minClusterSize := opts.MinClusterSize
	if minClusterSize <= 0 {
		minClusterSize = defaultMinClusterSize
	}
	minSharedTopicTokens := opts.MinSharedTopicTokens
	if minSharedTopicTokens <= 0 {
		minSharedTopicTokens = defaultMinSharedTopicTokens
	}

	lessonsForScan := filterClusterLessons(lessons, opts, window)
	result.ScannedLessons = len(lessonsForScan)
	if len(lessonsForScan) == 0 {
		return result
	}

	parent := make([]int, len(lessonsForScan))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	join := func(i, j int) {
		ri := find(i)
		rj := find(j)
		if ri == rj {
			return
		}
		if ri < rj {
			parent[rj] = ri
			return
		}
		parent[ri] = rj
	}

	for i := 0; i < len(lessonsForScan); i++ {
		for j := i + 1; j < len(lessonsForScan); j++ {
			if lessonsForScan[i].record.Source != lessonsForScan[j].record.Source {
				continue
			}
			if lessonsForScan[j].at.Sub(lessonsForScan[i].at) > window {
				continue
			}
			if sharedSignalCount(lessonsForScan[i].sigSet, lessonsForScan[j].sigSet) < minSharedTopicTokens {
				continue
			}
			join(i, j)
		}
	}

	groups := make(map[int][]clusterLesson)
	for i, lesson := range lessonsForScan {
		groups[find(i)] = append(groups[find(i)], lesson)
	}

	matched := 0
	clusters := make([]LessonCluster, 0, len(groups))
	for _, group := range groups {
		if len(group) < minClusterSize {
			continue
		}
		clusters = append(clusters, buildLessonCluster(group))
		matched += len(group)
	}
	sort.Slice(clusters, func(i, j int) bool {
		if clusters[i].WindowStart == clusters[j].WindowStart {
			return clusters[i].Key < clusters[j].Key
		}
		return clusters[i].WindowStart < clusters[j].WindowStart
	})
	result.MatchedLessons = matched
	result.Clusters = clusters
	return result
}

func filterClusterLessons(lessons []LessonRecord, opts ClusterScanOptions, window time.Duration) []clusterLesson {
	topicFilter := normalizeTopicToken(opts.Topic)
	sourceFilter := strings.TrimSpace(strings.ToLower(opts.Source))
	referenceTime := opts.ReferenceTime.UTC()
	if referenceTime.IsZero() {
		for _, lesson := range lessons {
			if ts, ok := parseLessonTime(lesson.CreatedAt); ok && ts.After(referenceTime) {
				referenceTime = ts
			}
		}
	}
	windowStart := referenceTime.Add(-window)

	filtered := make([]clusterLesson, 0, len(lessons))
	for _, lesson := range lessons {
		ts, ok := parseLessonTime(lesson.CreatedAt)
		if !ok {
			continue
		}
		if !referenceTime.IsZero() && (ts.Before(windowStart) || ts.After(referenceTime)) {
			continue
		}
		if sourceFilter != "" && strings.ToLower(strings.TrimSpace(lesson.Source)) != sourceFilter {
			continue
		}
		tags := stableTags(lesson.Tags)
		tokens := clusterTopicTokens(lesson)
		if topicFilter != "" && !containsString(tags, topicFilter) && !containsString(tokens, topicFilter) {
			continue
		}
		filtered = append(filtered, clusterLesson{
			record: lesson,
			at:     ts,
			tags:   tags,
			tokens: tokens,
			sigSet: clusterSignalSet(tags, tokens),
		})
	}
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].at.Equal(filtered[j].at) {
			return filtered[i].record.ID < filtered[j].record.ID
		}
		return filtered[i].at.Before(filtered[j].at)
	})
	return filtered
}

func buildLessonCluster(group []clusterLesson) LessonCluster {
	ids := make([]string, 0, len(group))
	tagCounts := make(map[string]int)
	tokenCounts := make(map[string]int)
	sourceSignals := make(map[string]struct{})
	start := group[0].at
	end := group[0].at
	for _, lesson := range group {
		ids = append(ids, lesson.record.ID)
		for _, tag := range lesson.tags {
			tagCounts[tag]++
		}
		for _, token := range lesson.tokens {
			tokenCounts[token]++
		}
		sourceSignals[lesson.record.Source] = struct{}{}
		if lesson.at.Before(start) {
			start = lesson.at
		}
		if lesson.at.After(end) {
			end = lesson.at
		}
	}
	sharedTags := repeatedSignals(tagCounts, len(group))
	sharedTokens := repeatedSignals(tokenCounts, len(group))
	topic := "cluster"
	if len(sharedTags) > 0 {
		topic = sharedTags[0]
	} else if len(sharedTokens) > 0 {
		topic = sharedTokens[0]
	}
	sourceList := make([]string, 0, len(sourceSignals))
	for source := range sourceSignals {
		sourceList = append(sourceList, source)
	}
	sort.Strings(ids)
	sort.Strings(sourceList)
	return LessonCluster{
		Key:              fmt.Sprintf("%s|%s|%s", group[0].record.Source, topic, start.UTC().Format(time.RFC3339)),
		Topic:            topic,
		Source:           group[0].record.Source,
		LessonIDs:        ids,
		LessonCount:      len(group),
		WindowStart:      start.UTC().Format(time.RFC3339),
		WindowEnd:        end.UTC().Format(time.RFC3339),
		SharedTags:       sharedTags,
		SharedTopicTerms: sharedTokens,
		SourceSignals:    sourceList,
	}
}

func clusterTopicTokens(record LessonRecord) []string {
	parts := []string{record.Situation, record.ErrorMessage, record.BetterApproach, record.Correction}
	seen := map[string]struct{}{}
	tokens := make([]string, 0, 8)
	for _, part := range parts {
		for _, token := range strings.FieldsFunc(strings.ToLower(part), func(r rune) bool {
			return (r < 'a' || r > 'z') && (r < '0' || r > '9')
		}) {
			token = normalizeTopicToken(token)
			if token == "" {
				continue
			}
			if _, skip := clusterStopWords[token]; skip {
				continue
			}
			if _, ok := seen[token]; ok {
				continue
			}
			seen[token] = struct{}{}
			tokens = append(tokens, token)
		}
	}
	sort.Strings(tokens)
	return tokens
}

func clusterSignalSet(tags, tokens []string) map[string]struct{} {
	signals := make(map[string]struct{}, len(tags)+len(tokens))
	for _, tag := range tags {
		signals["tag:"+tag] = struct{}{}
	}
	for _, token := range tokens {
		signals["token:"+token] = struct{}{}
	}
	return signals
}

func repeatedSignals(counts map[string]int, minCount int) []string {
	shared := make([]string, 0, len(counts))
	for signal, count := range counts {
		if count >= minCount {
			shared = append(shared, signal)
		}
	}
	sort.Strings(shared)
	return shared
}

func sharedSignalCount(left, right map[string]struct{}) int {
	count := 0
	for signal := range left {
		if _, ok := right[signal]; ok {
			count++
		}
	}
	return count
}

func parseLessonTime(value string) (time.Time, bool) {
	ts, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}, false
	}
	return ts.UTC(), true
}

func normalizeTopicToken(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if len(value) < 4 {
		return ""
	}
	return value
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
