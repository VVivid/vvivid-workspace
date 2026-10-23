package main

import (
	"fmt"
	"net/http"
	"time"

	trace "github.com/xtrace-example/traceplugin"
)

type Replica struct {
	name            string
	imageAge        time.Duration
	cachedImage     string
	cachedImageTime string
	emit            func(trace.Report)
}

func NewReplica(name string, imageAge time.Duration, emit func(trace.Report)) (*Replica, error) {
	if (name != "A" && name != "B") || imageAge < 0 {
		return nil, fmt.Errorf("replica must be A or B; image age must be nonnegative")
	}
	r := &Replica{name: name, imageAge: imageAge, emit: emit}
	// Both caches are rendered once. IMAGE_AGE controls the baked-in version.
	r.cachedImageTime = time.Now().UTC().Add(-imageAge).Format(time.RFC3339Nano)
	r.cachedImage = catImage(r.cachedImageTime)
	return r, nil
}

func (r *Replica) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /cat.svg", r.serveCat)
	return mux
}

func (r *Replica) serveCat(w http.ResponseWriter, req *http.Request) {
	taskID, parentID := req.Header.Get(trace.TaskHeader), req.Header.Get(trace.OpHeader)
	if !trace.ValidID(taskID) || !trace.ValidID(parentID) {
		http.Error(w, "missing or invalid X-Trace metadata", http.StatusBadRequest)
		return
	}
	opID, err := trace.NewID()
	if err != nil {
		http.Error(w, "cannot create operation ID", http.StatusInternalServerError)
		return
	}
	servedAt := time.Now().UTC()
	r.emit(trace.Report{
		TaskID: taskID, OpID: opID, ParentID: parentID, EdgeType: "NEXT",
		Service: "replica-" + r.name, Timestamp: servedAt.Format(time.RFC3339Nano),
		Details: map[string]string{
			"replica": r.name, "cache_seed_age": r.imageAge.String(), "cache_hit": "true",
			"image_timestamp": r.cachedImageTime, "path": req.URL.Path,
		},
	})
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set(trace.TaskHeader, taskID)
	w.Header().Set("X-Replica", r.name)
	w.Header().Set("X-Cache-Hit", "true")
	w.Header().Set("X-Image-Timestamp", r.cachedImageTime)
	_, _ = fmt.Fprint(w, r.cachedImage)
}

func catImage(timestamp string) string {
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 640 380" role="img" aria-label="Cat with image timestamp">
<rect width="640" height="380" rx="20" fill="#f3ead7"/>
<path d="M205 120 L190 40 L265 90 Q320 65 375 90 L450 40 L435 120 Q470 160 440 235 Q415 290 320 290 Q225 290 200 235 Q170 160 205 120" fill="#df9c51" stroke="#493c35" stroke-width="6"/>
<path d="M255 167 Q270 153 285 167 M355 167 Q370 153 385 167" fill="none" stroke="#493c35" stroke-width="6" stroke-linecap="round"/>
<path d="M307 192 L333 192 L320 207 Z" fill="#493c35"/>
<path d="M320 207 L320 222 M320 222 Q298 240 283 219 M320 222 Q342 240 357 219 M190 190 L275 200 M190 220 L275 211 M450 190 L365 200 M450 220 L365 211" fill="none" stroke="#493c35" stroke-width="4"/>
<text x="320" y="330" text-anchor="middle" font-family="monospace" font-size="15" fill="#493c35">Image timestamp: %s</text>
</svg>`, timestamp)
}
