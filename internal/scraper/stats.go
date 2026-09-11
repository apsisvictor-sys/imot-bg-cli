package scraper

import (
	"math"
	"sort"
	"strings"
)

// snippetLen is the description length used by the quiet projection.
const snippetLen = 120

// ComputeStats summarizes a listing set: price distribution, price per sqm,
// and agency/private split. Listings without a price or usable size are
// excluded from the respective aggregates but counted in Count.
func ComputeStats(listings []Listing) SearchStats {
	s := SearchStats{Count: len(listings)}

	var prices []float64
	var perSqm []float64
	for _, l := range listings {
		if strings.TrimSpace(l.Agency) == "" {
			s.PrivateCount++
		} else {
			s.AgencyCount++
		}
		if l.PriceEUR <= 0 {
			continue
		}
		prices = append(prices, float64(l.PriceEUR))
		if l.SizeSqM > 0 {
			perSqm = append(perSqm, float64(l.PriceEUR)/float64(l.SizeSqM))
		}
	}

	s.PricedCount = len(prices)
	if len(prices) == 0 {
		return s
	}

	sort.Float64s(prices)
	s.MeanEUR = round1(mean(prices))
	s.MedianEUR = round1(percentile(prices, 50))
	s.P25EUR = round1(percentile(prices, 25))
	s.P75EUR = round1(percentile(prices, 75))
	s.MinEUR = int(prices[0])
	s.MaxEUR = int(prices[len(prices)-1])

	if len(perSqm) > 0 {
		sort.Float64s(perSqm)
		s.MedianEURPerSqM = round1(percentile(perSqm, 50))
	}
	return s
}

// ToSlimListings projects listings onto the quiet-mode shape.
func ToSlimListings(listings []Listing) []SlimListing {
	out := make([]SlimListing, 0, len(listings))
	for _, l := range listings {
		slim := SlimListing{
			ID:           l.ID,
			Type:         l.Type,
			Neighborhood: l.Neighborhood,
			PriceEUR:     l.PriceEUR,
			SizeSqM:      l.SizeSqM,
			Floor:        l.Floor,
			Phone:        l.Phone,
			Snippet:      snippet(l.Description, snippetLen),
			URL:          l.URL,
		}
		if l.SizeSqM > 0 && l.PriceEUR > 0 {
			slim.PricePerSqM = round1(float64(l.PriceEUR) / float64(l.SizeSqM))
		}
		if strings.TrimSpace(l.Agency) == "" {
			slim.Seller = "private"
		} else {
			slim.Seller = "agency"
		}
		out = append(out, slim)
	}
	return out
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

// percentile returns the p-th percentile (0-100) using linear interpolation.
// The input slice must be sorted.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	rank := (p / 100) * float64(len(sorted)-1)
	lower := int(math.Floor(rank))
	upper := lower + 1
	if upper >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	weight := rank - float64(lower)
	return sorted[lower]*(1-weight) + sorted[upper]*weight
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}

func snippet(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
