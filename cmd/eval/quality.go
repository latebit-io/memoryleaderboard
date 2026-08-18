package main

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

var sourceMarker = regexp.MustCompile(`\[\[AML_SOURCE:([A-Za-z0-9._:-]+)\]\]`)

func (e evaluator) quality(path string, defaultTopK int) int {
	fixture, err := loadFixture(path)
	if err != nil {
		writef(e.err, "ERROR %v\n", err)
		return 2
	}
	run := runID()
	records := make(map[string]fixtureRecord, len(fixture.Records))
	for _, record := range fixture.Records {
		records[record.ID] = record
	}
	operationalFailures := 0
	writef(e.out, "FIXTURE name=%s records=%d queries=%d\n", fixture.Name, len(records), len(fixture.Queries))
	for _, record := range fixture.Records {
		payload := addPayload(
			"quality-"+run+"-"+record.ID,
			qualifyUser(run, record.UserID),
			record.SessionID,
			record.Content+"\n\n[[AML_SOURCE:"+record.ID+"]]",
			record.Timestamp,
		)
		res := e.client.request("/add", payload)
		if res.status != 200 {
			operationalFailures++
			writef(e.out, "FAIL Add source=%s status=%d body=%q\n", record.ID, res.status, truncate(res.body, 200))
		}
	}
	if operationalFailures != 0 {
		writef(e.out, "SUMMARY mode=quality operational_failures=%d; scoring skipped\n", operationalFailures)
		return 1
	}

	positiveQueries := 0
	recallAnySum := 0.0
	recallAllSum := 0.0
	ndcgSum := 0.0
	mrrSum := 0.0
	evidenceFound := 0
	evidenceTotal := 0
	returnedTotal := 0
	unmappedTotal := 0
	negativeTotal := 0
	isolationTotal := 0
	latencies := make([]float64, 0, len(fixture.Queries))

	for _, query := range fixture.Queries {
		topK := effectiveTopK(query, defaultTopK)
		res := e.client.request("/search", searchRequest{Query: query.Query, UserID: qualifyUser(run, query.UserID), TopK: topK})
		latencies = append(latencies, res.elapsedMS)
		body, bodyError := decodeResponse(res)
		var errors []string
		if res.status == 200 && body != nil {
			errors = resultErrors(body, topK)
		} else {
			errors = []string{responseDetail(res, body, bodyError)}
		}
		if len(errors) != 0 {
			operationalFailures++
			writef(e.out, "FAIL query=%s latency_ms=%.1f errors=%s\n", query.ID, res.elapsedMS, strings.Join(errors, "; "))
		}

		data, ok := responseData(body)
		if !ok {
			continue
		}
		items := make([]map[string]any, 0, len(data))
		validContent := true
		for _, raw := range data {
			item, ok := raw.(map[string]any)
			if !ok {
				validContent = false
				break
			}
			if _, ok := item["content"].(string); !ok {
				validContent = false
				break
			}
			items = append(items, item)
		}
		if !validContent {
			continue
		}

		relevant := stringSet(query.Relevant)
		negative := stringSet(query.Negative)
		rankedSources := make([]map[string]bool, 0, len(items))
		retrieved := make(map[string]bool)
		queryUnmapped := 0
		queryNegative := 0
		queryIsolation := 0
		for _, result := range items {
			sources := make(map[string]bool)
			for _, match := range sourceMarker.FindAllStringSubmatch(result["content"].(string), -1) {
				if _, exists := records[match[1]]; exists {
					sources[match[1]] = true
					retrieved[match[1]] = true
				}
			}
			rankedSources = append(rankedSources, sources)
			if len(sources) == 0 {
				queryUnmapped++
			}
			if setsIntersect(sources, negative) {
				queryNegative++
			}
			for source := range sources {
				if records[source].UserID != query.UserID {
					queryIsolation++
					break
				}
			}
		}

		hits := setIntersection(retrieved, relevant)
		if len(relevant) != 0 {
			positiveQueries++
			if len(hits) != 0 {
				recallAnySum++
			}
			if len(hits) == len(relevant) {
				recallAllSum++
			}
			evidenceFound += len(hits)
			evidenceTotal += len(relevant)
			rankedSeen := make(map[string]bool)
			binaryRanks := make([]int, 0, len(rankedSources))
			for _, sources := range rankedSources {
				newHit := false
				for source := range sources {
					if relevant[source] && !rankedSeen[source] {
						newHit = true
					}
					if relevant[source] {
						rankedSeen[source] = true
					}
				}
				if newHit {
					binaryRanks = append(binaryRanks, 1)
				} else {
					binaryRanks = append(binaryRanks, 0)
				}
			}
			ideal := dcg(ones(min(len(relevant), topK)))
			if ideal != 0 {
				ndcgSum += dcg(binaryRanks) / ideal
			}
			for index, hit := range binaryRanks {
				if hit != 0 {
					mrrSum += 1 / float64(index+1)
					break
				}
			}
		}

		returnedTotal += len(data)
		unmappedTotal += queryUnmapped
		negativeTotal += queryNegative
		isolationTotal += queryIsolation
		writef(
			e.out,
			"QUERY id=%s scenario=%s top_k=%d latency_ms=%.1f returned=%d relevant=%d/%d negative=%d unmapped=%d isolation=%d\n",
			query.ID, query.Scenario, topK, res.elapsedMS, len(data), len(hits), len(relevant), queryNegative, queryUnmapped, queryIsolation,
		)
	}

	queryDenominator := max(positiveQueries, 1)
	resultDenominator := max(returnedTotal, 1)
	cutoff := metricCutoffLabel(fixture.Queries, defaultTopK)
	evidenceRecall := 0.0
	if evidenceTotal != 0 {
		evidenceRecall = float64(evidenceFound) / float64(evidenceTotal)
	}
	metrics := []struct {
		name  string
		value float64
	}{
		{name: "recall_any@" + cutoff, value: recallAnySum / float64(queryDenominator)},
		{name: "recall_all@" + cutoff, value: recallAllSum / float64(queryDenominator)},
		{name: "evidence_recall@" + cutoff, value: evidenceRecall},
		{name: "nDCG@" + cutoff, value: ndcgSum / float64(queryDenominator)},
		{name: "MRR", value: mrrSum / float64(queryDenominator)},
		{name: "unmapped_rate", value: float64(unmappedTotal) / float64(resultDenominator)},
		{name: "negative_rate", value: float64(negativeTotal) / float64(resultDenominator)},
	}
	writeText(e.out, "METRICS")
	for _, metric := range metrics {
		writef(e.out, " %s=%.4f", metric.name, metric.value)
	}
	writeLine(e.out)
	if len(latencies) != 0 {
		writef(
			e.out,
			"LATENCY count=%d mean_ms=%.1f p50_ms=%.1f p95_ms=%.1f\n",
			len(latencies), mean(latencies), percentile(latencies, .50), percentile(latencies, .95),
		)
	}
	writef(
		e.out,
		"SUMMARY mode=quality operational_failures=%d isolation_violations=%d unmapped=%d/%d negative=%d/%d\n",
		operationalFailures, isolationTotal, unmappedTotal, returnedTotal, negativeTotal, returnedTotal,
	)
	if operationalFailures != 0 || isolationTotal != 0 {
		return 1
	}
	return 0
}

func qualifyUser(run, userID string) string {
	return "local-quality-" + run + "-" + userID
}

func effectiveTopK(query fixtureQuery, defaultTopK int) int {
	if query.TopK != nil {
		return *query.TopK
	}
	return defaultTopK
}

func metricCutoffLabel(queries []fixtureQuery, defaultTopK int) string {
	cutoffs := make(map[int]bool)
	for _, query := range queries {
		cutoffs[effectiveTopK(query, defaultTopK)] = true
	}
	if len(cutoffs) != 1 {
		return "mixed"
	}
	for cutoff := range cutoffs {
		return fmt.Sprintf("%d", cutoff)
	}
	return fmt.Sprintf("%d", defaultTopK)
}

func dcg(relevances []int) float64 {
	result := 0.0
	for rank, relevance := range relevances {
		result += float64(relevance) / math.Log2(float64(rank+2))
	}
	return result
}

func ones(count int) []int {
	result := make([]int, count)
	for index := range result {
		result[index] = 1
	}
	return result
}

func responseData(body any) ([]any, bool) {
	object, ok := body.(map[string]any)
	if !ok {
		return nil, false
	}
	data, ok := object["data"].([]any)
	return data, ok
}

func stringSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func setsIntersect(left, right map[string]bool) bool {
	for value := range left {
		if right[value] {
			return true
		}
	}
	return false
}

func setIntersection(left, right map[string]bool) map[string]bool {
	result := make(map[string]bool)
	for value := range left {
		if right[value] {
			result[value] = true
		}
	}
	return result
}

func percentile(values []float64, fraction float64) float64 {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	index := max(0, int(math.Ceil(fraction*float64(len(ordered))))-1)
	return ordered[index]
}

func mean(values []float64) float64 {
	total := 0.0
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}

func truncate(value []byte, length int) []byte {
	if len(value) <= length {
		return value
	}
	return value[:length]
}
