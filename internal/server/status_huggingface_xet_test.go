package server

import (
	"strings"
	"testing"
)

func TestUnitHuggingFaceXetRouteStatuses(t *testing.T) {
	w := newXetWorld(t)
	write, _ := publishXetFixture(t, w, "hf")
	read := xetTokenFor(t, w, "hf", "acme/demo", "read", "reader")
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"POST", "/v1/xorbs/default/bad", 422}, {"POST", "/v1/shards", 422}, {"POST", "/v2/shards", 422}, {"HEAD", "/v1/xorbs/default/bad", 400}, {"GET", "/v1/chunks/bad/" + xetFixtureHash, 400}, {"GET", "/v1/chunks/default/bad", 400},
	} {
		wantStatus(t, xetCall(w, write, tc.method, tc.path, nil), tc.want)
		wantStatus(t, xetCall(w, xetTestToken{CAS: write.CAS}, tc.method, tc.path, nil), 403)
	}
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/v1/reconstructions/bad", 400},
		{"GET", "/v2/reconstructions/bad", 400},
		{"GET", "/data/" + xetFixtureFile + "/bad/0/1", 400},
		{"GET", "/data/" + xetFixtureFile + "/0/-1/1", 400},
		{"GET", "/data/" + xetFixtureFile + "/0/0/9000", 400},
		{"GET", "/v1/reconstructions/" + xetFixtureFile + "?other=x", 403},
		{"GET", "/v1/reconstructions/" + xetFixtureFile + "?token=" + read.Token, 403},
	} {
		wantStatus(t, xetCall(w, read, tc.method, tc.path, nil), tc.want)
		wantStatus(t, xetCall(w, xetTestToken{CAS: read.CAS}, tc.method, tc.path, nil), 403)
	}
	for _, header := range []string{"bytes=0-10", "bytes=131070-131080", "bytes=0-18446744073709551615"} {
		wantStatus(
			t,
			w.do(
				"GET",
				read.CAS+"/v2/reconstructions/"+xetFixtureFile,
				"",
				"",
				"Authorization",
				"Bearer "+read.Token,
				"Range",
				header,
			),
			200,
		)
	}
	wantStatus(
		t,
		w.do(
			"GET",
			read.CAS+"/v1/reconstructions/"+xetFixtureFile,
			"",
			"",
			"Authorization",
			"Bearer "+read.Token,
			"Range",
			"bytes=9-1",
		),
		416,
	)
	for _, suffix := range []string{"?other=1", "?file=../escape", "?file=a&file=b"} {
		wantStatus(
			t,
			w.do(
				"GET",
				"/artifactory/api/huggingfaceml/hf/api/models/acme/demo/xet-read-token/main"+suffix,
				"admin",
				"",
			),
			400,
		)
	}
	wantStatus(
		t,
		w.do(
			"GET",
			"/artifactory/api/huggingfaceml/hf/api/models/acme/demo/xet-write-token/main",
			"user",
			"",
		),
		403,
	)
	wantStatus(
		t,
		w.do(
			"GET",
			"/artifactory/api/huggingfaceml/hf/api/models/acme/missing/xet-read-token/main",
			"admin",
			"",
		),
		404,
	)
	malformed := read
	malformed.Token = strings.Repeat("a", 4097)
	wantStatus(t, xetCall(w, malformed, "GET", "/v2/reconstructions/"+xetFixtureFile, nil), 403)
}
