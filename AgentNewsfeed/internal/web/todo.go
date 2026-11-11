package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"agentnewsfeed/internal/feed"
	"agentnewsfeed/internal/identity"
	"agentnewsfeed/internal/store/sqlite"
)

func (h *handler) todo(w http.ResponseWriter, r *http.Request) {
	data := bootstrap{Configured: h.auth != nil, Section: "todo", View: "feed", SignInURL: "/auth/login?return_to=" + url.QueryEscape(r.URL.RequestURI())}
	status := http.StatusOK
	if h.auth == nil {
		status = http.StatusServiceUnavailable
	} else if p, csrf, err := h.auth.BrowserSession(r); err == nil {
		tasks, err := h.service.ListTodo(r.Context(), p)
		if err != nil {
			writeTodoError(w, err)
			return
		}
		data.Todo = &tasks
		data.Authenticated, data.CSRF, data.CanWrite = true, csrf, p.Allows(identity.ArticlesWrite)
	}
	h.renderPage(w, r, status, data)
}

func (h *handler) todoForm(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.FromContext(r.Context())
	action := r.PostForm.Get("action")
	id := r.PostForm.Get("task_id")
	var err error
	if action == "add" {
		err = h.addTodo(r, p, id)
	} else if action == "reorder" {
		err = h.reorderTodo(r, p, id)
	} else {
		err = h.changeTodo(r, p, id, sqlite.TodoAction(action))
	}
	if err != nil {
		writeTodoError(w, err)
		return
	}
	respondToStateForm(w, r, "/todo")
}

func (h *handler) reorderTodo(r *http.Request, p identity.Principal, id string) error {
	if !exactTodoFields(r.PostForm, "action", "csrf_token", "task_id", "expected_revision", "neighbor_id", "neighbor_revision", "direction", "idempotency_key") {
		return feed.ErrInvalid
	}
	revision, err := strconv.ParseInt(r.PostForm.Get("expected_revision"), 10, 64)
	if err != nil {
		return feed.ErrInvalid
	}
	neighborRevision, err := strconv.ParseInt(r.PostForm.Get("neighbor_revision"), 10, 64)
	if err != nil {
		return feed.ErrInvalid
	}
	return h.service.ReorderTodo(r.Context(), p, r.PostForm.Get("idempotency_key"), sqlite.TodoMove{
		TaskID: id, ExpectedRevision: revision, NeighborID: r.PostForm.Get("neighbor_id"),
		NeighborRevision: neighborRevision, Direction: r.PostForm.Get("direction"),
	})
}

func (h *handler) addTodo(r *http.Request, p identity.Principal, id string) error {
	if !exactTodoFields(r.PostForm, "action", "csrf_token", "task_id", "title", "tags", "estimate_minutes") {
		return feed.ErrInvalid
	}
	minutes := 0
	if estimate := r.PostForm.Get("estimate_minutes"); estimate != "" {
		var err error
		minutes, err = strconv.Atoi(estimate)
		if err != nil {
			return feed.ErrInvalid
		}
	}
	return h.service.AddTodo(r.Context(), p, id, r.PostForm.Get("title"), r.PostForm.Get("tags"), minutes)
}

func (h *handler) changeTodo(r *http.Request, p identity.Principal, id string, action sqlite.TodoAction) error {
	if !exactTodoFields(r.PostForm, "action", "csrf_token", "task_id", "expected_revision") {
		return feed.ErrInvalid
	}
	revision, err := strconv.ParseInt(r.PostForm.Get("expected_revision"), 10, 64)
	if err != nil {
		return feed.ErrInvalid
	}
	return h.service.ChangeTodoState(r.Context(), p, id, revision, action)
}

func exactTodoFields(form url.Values, names ...string) bool {
	if len(form) != len(names) {
		return false
	}
	for _, name := range names {
		if len(form[name]) != 1 {
			return false
		}
	}
	return true
}

func writeTodoError(w http.ResponseWriter, err error) {
	message := "The task request could not be completed."
	switch {
	case errors.Is(err, feed.ErrInvalid):
		message = "Invalid task request."
	case errors.Is(err, sqlite.ErrNotFound):
		message = "Task not found."
	case errors.Is(err, sqlite.ErrConflict), errors.Is(err, sqlite.ErrIdempotencyConflict):
		message = "This task changed. Reload the checklist before trying again."
	}
	writeSafeError(w, err, message)
}
