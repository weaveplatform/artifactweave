package remote

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func TestUnitP2ExternalChildNeverReceivesRepositoryCredentials(t *testing.T) {
	var authorization, custom, cookie string
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		custom = r.Header.Get("X-Secret")
		cookie = r.Header.Get("Cookie")
		w.WriteHeader(http.StatusOK)
	}))
	defer child.Close()
	base, err := url.Parse("http://base.example")
	if err != nil {
		t.Fatal(err)
	}
	all := true
	c := repo.Config{
		URL:               base.String(),
		Username:          "upstream",
		Password:          "private",
		AllowAnyHostAuth:  &all,
		CustomHTTPHeaders: []repo.CustomHTTPHeader{{Name: "X-Secret", Value: "private"}},
	}
	security := &sourceSecurity{base: base, config: c}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(child.URL)
	jar.SetCookies(u, []*http.Cookie{{Name: "token", Value: "private"}})
	client, err := NewClient(c, Options{
		Prepare:  security.prepare,
		CheckURL: func(context.Context, *url.URL) error { return nil },
		Jar:      jar,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	res, err := client.Do(t.Context(), Request{
		Method:      http.MethodGet,
		ExternalURL: child.URL, NoCrossOriginAuth: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if authorization != "" || custom != "" || cookie != "" {
		t.Fatalf(
			"external child received repository secrets: auth=%q header=%q cookie=%q",
			authorization,
			custom,
			cookie,
		)
	}
}

func TestUnitP2SameOriginChildKeepsConfiguredAuthentication(t *testing.T) {
	var authorization, cookie string
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		cookie = r.Header.Get("Cookie")
		w.WriteHeader(http.StatusOK)
	}))
	defer site.Close()
	base, _ := url.Parse(site.URL)
	c := repo.Config{URL: site.URL, Username: "upstream", Password: "private"}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(base, []*http.Cookie{{Name: "session", Value: "needed"}})
	security := &sourceSecurity{base: base, config: c}
	client, err := NewClient(c, Options{
		Prepare:  security.prepare,
		CheckURL: func(context.Context, *url.URL) error { return nil },
		Jar:      jar,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	res, err := client.Do(t.Context(), Request{
		Method:      http.MethodGet,
		ExternalURL: site.URL + "/child", NoCrossOriginAuth: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if authorization == "" || cookie != "session=needed" {
		t.Fatalf(
			"same-origin child lost source authentication: auth=%q cookie=%q",
			authorization,
			cookie,
		)
	}
}
