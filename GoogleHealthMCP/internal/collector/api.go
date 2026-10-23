package collector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
)

type API struct {
	OAuth *OAuth
	Base  string
}

func NewAPI(o *OAuth) *API { return &API{OAuth: o, Base: "https://health.googleapis.com"} }

func (a *API) request(ctx context.Context, method, path string, body []byte, out any) error {
	token, err := a.OAuth.Access(ctx, false)
	if err != nil {
		return err
	}
	err = a.OAuth.HTTP.JSON(ctx, method, a.Base+path, token, "application/json", body, true, out)
	if Code(err) != "unauthorized" || err == nil {
		return err
	}
	token, err = a.OAuth.Access(ctx, true)
	if err != nil {
		return err
	}
	err = a.OAuth.HTTP.JSON(ctx, method, a.Base+path, token, "application/json", body, true, out)
	if err != nil && Code(err) == "unauthorized" {
		a.OAuth.Tokens.Verified, a.OAuth.Tokens.Problem = false, "reauthorization_required"
		if saveErr := a.OAuth.Save(); saveErr != nil {
			return saveErr
		}
		return &Error{Code: "reauthorization_required"}
	}
	return err
}

func (a *API) Identity(ctx context.Context) (string, error) {
	var identity struct {
		HealthUserID string `json:"healthUserId"`
	}
	if err := a.request(ctx, http.MethodGet, "/v4/users/me/identity", nil, &identity); err != nil {
		return "", err
	}
	if !regexp.MustCompile(`^[A-Za-z0-9-]{1,63}$`).MatchString(identity.HealthUserID) {
		return "", &Error{Code: "invalid_identity_response"}
	}
	return identity.HealthUserID, nil
}

type Page struct {
	Data   []json.RawMessage `json:"dataPoints"`
	Rollup []json.RawMessage `json:"rollupDataPoints"`
	Next   string            `json:"nextPageToken"`
}

func (a *API) Page(ctx context.Context, m Metric, w Window, token string, limit int) (Page, error) {
	pageSize := 10000
	if m.Kind == "exercise" || m.Kind == "sleep" {
		pageSize = 25
	}
	if limit > 0 {
		pageSize = min(pageSize, limit)
	}
	path := "/" + m.Version + "/users/me/dataTypes/" + m.Name + "/dataPoints"
	var page Page
	if m.Method == "dailyRollUp" {
		if w.Since == "" {
			return page, &Error{Code: "aggregate_floor_required"}
		}
		civil := func(day string) map[string]any {
			t, _ := ParseDate(day)
			return map[string]any{"date": map[string]int{"year": t.Year(), "month": int(t.Month()), "day": t.Day()}, "time": map[string]int{}}
		}
		body, err := json.Marshal(map[string]any{
			"range": map[string]any{"start": civil(w.Since), "end": civil(w.Until)}, "windowSizeDays": 1,
			"pageSize": pageSize, "pageToken": token, "dataSourceFamily": "users/me/dataSourceFamilies/google-sources",
		})
		if err != nil {
			return page, &Error{Code: "request_failed"}
		}
		if err := a.request(ctx, http.MethodPost, path+":dailyRollUp", body, &page); err != nil {
			return page, err
		}
		page.Data = page.Rollup
	} else {
		if m.Method == "reconcile" {
			path += ":reconcile"
		}
		query := url.Values{"filter": {w.Filter(m)}, "pageSize": {strconv.Itoa(pageSize)}, "dataSourceFamily": {"users/me/dataSourceFamilies/google-sources"}}
		if token != "" {
			query.Set("pageToken", token)
		}
		if err := a.request(ctx, http.MethodGet, path+"?"+query.Encode(), nil, &page); err != nil {
			return page, err
		}
	}
	return page, nil
}
