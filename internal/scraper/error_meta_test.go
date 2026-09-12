package scraper

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// A 429 with Retry-After must reach the consumer as typed error metadata, not
// only as a free-text "HTTP 429" message. The collector's retry policy reads
// the parsed seconds to schedule the next attempt.
func TestFetchDetailNowReportsRetryAfterAndHTTPStatus(t *testing.T) {
	client := &Client{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		resp := htmlResponse(http.StatusTooManyRequests, "too many requests")
		resp.Header.Set("Retry-After", "30")
		return resp, nil
	})}}

	_, err := client.fetchDetailNow(legitDetailURL)
	de := requireDetailError(t, err)
	if de.Kind != DetailErrorFetchFailed {
		t.Fatalf("kind = %q, want %q", de.Kind, DetailErrorFetchFailed)
	}
	if de.HTTPStatus != http.StatusTooManyRequests {
		t.Errorf("http_status = %d, want 429", de.HTTPStatus)
	}
	if de.RetryAfterSeconds == nil || *de.RetryAfterSeconds != 30 {
		t.Errorf("retry_after_seconds = %v, want 30", de.RetryAfterSeconds)
	}
	if de.EffectiveURL != legitDetailURL {
		t.Errorf("effective_url = %q, want %q", de.EffectiveURL, legitDetailURL)
	}
}

// A response with no Retry-After must omit the field, not invent a pause.
func TestFetchDetailNowOmitsUnprovenRetryAfter(t *testing.T) {
	client := clientReturning("not found", http.StatusNotFound)

	_, err := client.fetchDetailNow(legitDetailURL)
	de := requireDetailError(t, err)
	if de.HTTPStatus != http.StatusNotFound {
		t.Errorf("http_status = %d, want 404", de.HTTPStatus)
	}
	if de.RetryAfterSeconds != nil {
		t.Errorf("retry_after_seconds = %v, want nil for a response without the header", *de.RetryAfterSeconds)
	}
}

// A redirect can land on a different URL; effective_url must name where the
// source actually served the answer, not the URL that was requested.
func TestFetchDetailNowRecordsEffectiveURLAfterRedirect(t *testing.T) {
	final, err := url.Parse("https://www.imot.bg/obiava-9x999999999999999-dvustaen")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	client := &Client{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		resp := htmlResponse(http.StatusServiceUnavailable, "blocked")
		resp.Request = &http.Request{URL: final}
		return resp, nil
	})}}

	_, err = client.fetchDetailNow(legitDetailURL)
	de := requireDetailError(t, err)
	if de.EffectiveURL != final.String() {
		t.Errorf("effective_url = %q, want %q", de.EffectiveURL, final.String())
	}
	if de.RequestedURL != legitDetailURL {
		t.Errorf("requested_url = %q, want %q", de.RequestedURL, legitDetailURL)
	}
}

func TestParseRetryAfterForms(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)

	if got := parseRetryAfter("", now); got != nil {
		t.Errorf("empty header = %v, want nil", *got)
	}
	if got := parseRetryAfter("not-a-delay", now); got != nil {
		t.Errorf("garbage header = %v, want nil", *got)
	}
	if got := parseRetryAfter("-5", now); got != nil {
		t.Errorf("negative delta = %v, want nil", *got)
	}
	zero := parseRetryAfter("0", now)
	if zero == nil || *zero != 0 {
		t.Errorf("zero delta = %v, want a proven 0", zero)
	}
	in90 := parseRetryAfter(now.Add(90*time.Second).UTC().Format(http.TimeFormat), now)
	if in90 == nil || *in90 < 85 || *in90 > 90 {
		t.Errorf("HTTP-date Retry-After = %v, want ~90", in90)
	}
	past := parseRetryAfter(now.Add(-time.Minute).UTC().Format(http.TimeFormat), now)
	if past == nil || *past != 0 {
		t.Errorf("past HTTP-date = %v, want clamped 0", past)
	}
}

// A proven zero must serialize as 0 rather than being dropped by omitempty, and
// an absent value must not appear at all.
func TestDetailErrorRetryAfterJSONPresence(t *testing.T) {
	zero := 0
	withZero, err := json.Marshal(&DetailError{Kind: DetailErrorFetchFailed, RetryAfterSeconds: &zero})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(withZero), `"retry_after_seconds":0`) {
		t.Errorf("zero retry_after_seconds was omitted: %s", withZero)
	}

	without, err := json.Marshal(&DetailError{Kind: DetailErrorFetchFailed})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(without), "retry_after_seconds") {
		t.Errorf("absent retry_after_seconds was emitted: %s", without)
	}
	if strings.Contains(string(without), "effective_url") {
		t.Errorf("absent effective_url was emitted: %s", without)
	}
}

// The offline reject path records the effective URL the caller proves, without
// changing the success payload.
func TestParseDetailPageWithMetaRecordsEffectiveURLOnReject(t *testing.T) {
	_, err := ParseDetailPageWithMeta(
		readFixture(t, "detail-challenge.html"),
		legitDetailURL,
		"https://www.imot.bg/blocked",
	)
	de := requireDetailError(t, err)
	if de.Kind != DetailErrorChallengePage {
		t.Fatalf("kind = %q, want %q", de.Kind, DetailErrorChallengePage)
	}
	if de.EffectiveURL != "https://www.imot.bg/blocked" {
		t.Errorf("effective_url = %q, want the served URL", de.EffectiveURL)
	}
	// The plain entry point stays compatible: no effective URL is invented.
	_, err = ParseDetailPage(readFixture(t, "detail-challenge.html"), legitDetailURL)
	de = requireDetailError(t, err)
	if de.EffectiveURL != "" {
		t.Errorf("ParseDetailPage effective_url = %q, want empty", de.EffectiveURL)
	}
}

// The presence contract is success-only metadata: a typed error payload keeps
// its established shape and must not grow contract_version or field_evidence.
func TestDetailErrorJSONHasNoSuccessMetadata(t *testing.T) {
	payload, err := json.Marshal(&DetailError{
		Kind:              DetailErrorWrongIdentity,
		RequestedURL:      legitDetailURL,
		RequestedAdvertID: "177425523801314",
		ObservedAdvertID:  "176754466608675",
		Message:           "wrong advert",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, forbidden := range []string{"field_evidence", "contract_version"} {
		if strings.Contains(string(payload), forbidden) {
			t.Errorf("error payload grew %q: %s", forbidden, payload)
		}
	}
}
