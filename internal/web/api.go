package web

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"

	"agentshoppinglist/internal/identity"
	"agentshoppinglist/internal/shopping"
	"agentshoppinglist/internal/store/sqlite"
)

func registerAPI(mux *http.ServeMux, auth *identity.Manager, service *shopping.Service) {
	read := func(pattern string, handler http.HandlerFunc) {
		mux.Handle(pattern, auth.BrowserMiddleware([]string{identity.Read}, handler))
	}
	write := func(pattern string, handler http.HandlerFunc) {
		mux.Handle(pattern, auth.BrowserMiddleware([]string{identity.ListsWrite}, handler))
	}
	read("GET /api/v1/me", func(w http.ResponseWriter, r *http.Request) {
		p, _ := identity.FromContext(r.Context())
		data, err := service.GetContext(r.Context(), p)
		respond(w, data, err)
	})
	read("GET /api/v1/products", func(w http.ResponseWriter, r *http.Request) {
		p, _ := identity.FromContext(r.Context())
		limit, err := queryLimit(r)
		if err != nil {
			respond(w, nil, err)
			return
		}
		data, err := service.SearchProducts(r.Context(), p, shopping.SearchInput{Query: r.URL.Query().Get("q"), Cursor: r.URL.Query().Get("cursor"), Limit: limit})
		respond(w, data, err)
	})
	read("GET /api/v1/products/{product_id}", func(w http.ResponseWriter, r *http.Request) {
		p, _ := identity.FromContext(r.Context())
		data, err := service.GetProduct(r.Context(), p, r.PathValue("product_id"))
		respond(w, data, err)
	})
	read("GET /api/v1/lists/active", func(w http.ResponseWriter, r *http.Request) {
		p, _ := identity.FromContext(r.Context())
		retailer := r.URL.Query().Get("retailer")
		if retailer == "" {
			context, err := service.GetContext(r.Context(), p)
			if err != nil {
				respond(w, nil, err)
				return
			}
			retailer = context.Preferences.PreferredRetailer
		}
		data, err := service.GetList(r.Context(), p, shopping.ListInput{Retailer: retailer})
		respond(w, data, err)
	})
	write("POST /api/v1/lists/{list_id}/items", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ProductID            string  `json:"product_id"`
			Quantity             string  `json:"quantity"`
			Merge                string  `json:"merge"`
			Retailer             *string `json:"retailer"`
			ExpectedListRevision int64   `json:"expected_list_revision"`
		}
		if err := decode(w, r, &in); err != nil {
			respond(w, nil, err)
			return
		}
		p, _ := identity.FromContext(r.Context())
		data, err := service.UpdateItems(r.Context(), p, shopping.UpdateItemsInput{
			WriteMeta:        requestMeta(w, r),
			ListID:           r.PathValue("list_id"),
			ExpectedRevision: in.ExpectedListRevision,
			Operations: []sqlite.ItemOperation{{
				Kind:      "add",
				ProductID: in.ProductID,
				Quantity:  new(in.Quantity),
				Merge:     in.Merge,
				Retailer:  in.Retailer,
			}},
		})
		respond(w, data, err)
	})
	write("PATCH /api/v1/lists/{list_id}/items/{item_id}", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Quantity             *string `json:"quantity"`
			PickedUp             *bool   `json:"picked_up"`
			ExpectedRevision     int64   `json:"expected_revision"`
			ExpectedListRevision int64   `json:"expected_list_revision"`
		}
		if err := decode(w, r, &in); err != nil {
			respond(w, nil, err)
			return
		}
		if (in.Quantity == nil) == (in.PickedUp == nil) {
			respond(w, nil, &shopping.ValidationError{Message: "Set exactly one of quantity or picked_up."})
			return
		}
		kind := "set_quantity"
		if in.PickedUp != nil {
			kind = "set_picked_up"
		}
		p, _ := identity.FromContext(r.Context())
		data, err := service.UpdateItems(r.Context(), p, shopping.UpdateItemsInput{
			WriteMeta:        requestMeta(w, r),
			ListID:           r.PathValue("list_id"),
			ExpectedRevision: in.ExpectedListRevision,
			Operations: []sqlite.ItemOperation{{
				Kind:             kind,
				ItemID:           r.PathValue("item_id"),
				Quantity:         in.Quantity,
				PickedUp:         in.PickedUp,
				ExpectedRevision: in.ExpectedRevision,
			}},
		})
		respond(w, data, err)
	})
	write("DELETE /api/v1/lists/{list_id}/items/{item_id}", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ExpectedRevision     int64 `json:"expected_revision"`
			ExpectedListRevision int64 `json:"expected_list_revision"`
		}
		if err := decode(w, r, &in); err != nil {
			respond(w, nil, err)
			return
		}
		p, _ := identity.FromContext(r.Context())
		data, err := service.UpdateItems(r.Context(), p, shopping.UpdateItemsInput{
			WriteMeta:        requestMeta(w, r),
			ListID:           r.PathValue("list_id"),
			ExpectedRevision: in.ExpectedListRevision,
			Operations: []sqlite.ItemOperation{{
				Kind:             "remove",
				ItemID:           r.PathValue("item_id"),
				ExpectedRevision: in.ExpectedRevision,
			}},
		})
		respond(w, data, err)
	})
}

func requestMeta(w http.ResponseWriter, r *http.Request) shopping.WriteMeta {
	return shopping.WriteMeta{RequestID: w.Header().Get("X-Request-ID"), IdempotencyKey: r.Header.Get("Idempotency-Key")}
}

func queryLimit(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, &shopping.ValidationError{Message: "Invalid limit."}
	}
	return value, nil
}

func decode(w http.ResponseWriter, r *http.Request, target any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return &shopping.ValidationError{Message: "Content-Type must be application/json."}
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return &shopping.ValidationError{Message: "Invalid or oversized JSON request."}
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return &shopping.ValidationError{Message: "Send one JSON object."}
	}
	return nil
}

func respond(w http.ResponseWriter, data any, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		status := http.StatusInternalServerError
		code := "request_failed"
		switch {
		case errors.Is(err, identity.ErrUnauthenticated):
			status = http.StatusUnauthorized
			code = "authentication_required"
		case errors.Is(err, identity.ErrForbidden):
			status = http.StatusForbidden
			code = "permission_denied"
		case errors.Is(err, sqlite.ErrNotFound):
			status = http.StatusNotFound
			code = "not_found"
		case errors.Is(err, sqlite.ErrConflict):
			status = http.StatusConflict
			code = "revision_conflict"
		case errors.Is(err, sqlite.ErrIdempotencyConflict):
			status = http.StatusConflict
			code = "idempotency_conflict"
		default:
			if _, ok := errors.AsType[*shopping.ValidationError](err); ok {
				status = http.StatusBadRequest
				code = "invalid_request"
			}
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": shopping.SafeError(err)})
		return
	}
	_ = json.NewEncoder(w).Encode(data)
}
