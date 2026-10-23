package main

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"slices"

	trace "github.com/xtrace-example/traceplugin"
)

type TreeRow struct {
	Report  trace.Report
	Depth   int
	Missing bool
}

type Worktree struct {
	TaskID   string
	Rows     []TreeRow
	Reports  []trace.Report
	Issues   []string
	Complete bool
}

// reconstruct links by operation IDs, independent of arrival order and clocks.
// Missing parents become visible placeholders; cycles cannot recurse forever.
func reconstruct(taskID string, reports []trace.Report) Worktree {
	tree := Worktree{TaskID: taskID, Reports: reports}
	reportsByOpID := make(map[string]trace.Report)
	childrenByParentID := make(map[string][]string)
	var ids, roots []string
	for _, report := range reports {
		reportsByOpID[report.OpID] = report
		ids = append(ids, report.OpID)
	}
	slices.Sort(ids)
	for _, id := range ids {
		report := reportsByOpID[id]
		if report.ParentID == "" {
			roots = append(roots, id)
			continue
		}
		childrenByParentID[report.ParentID] = append(childrenByParentID[report.ParentID], id)
		if _, present := reportsByOpID[report.ParentID]; !present {
			reportsByOpID[report.ParentID] = trace.Report{OpID: report.ParentID}
			roots = append(roots, report.ParentID)
			tree.Issues = append(tree.Issues, "Missing parent report: "+report.ParentID)
		}
	}
	slices.Sort(roots)
	visited, active := make(map[string]bool), make(map[string]bool)
	var visit func(string, int)
	visit = func(id string, depth int) {
		if active[id] {
			tree.Issues = append(tree.Issues, "Cycle in parent IDs at operation "+id)
			return
		}
		if visited[id] {
			return
		}
		visited[id], active[id] = true, true
		report := reportsByOpID[id]
		tree.Rows = append(tree.Rows, TreeRow{Report: report, Depth: depth, Missing: report.Service == ""})
		for _, child := range childrenByParentID[id] {
			visit(child, depth+1)
		}
		active[id] = false
	}
	for _, root := range roots {
		visit(root, 0)
	}
	for _, id := range ids {
		if !visited[id] {
			tree.Issues = append(tree.Issues, "No root reachable from operation "+id)
			visit(id, 0)
		}
	}
	// A reconstructed tree is complete only when it matches this demo's chain.
	var nginxReports, traefikReports, replicaReports []trace.Report
	for _, report := range reports {
		switch report.Service {
		case "nginx":
			nginxReports = append(nginxReports, report)
		case "traefik":
			traefikReports = append(traefikReports, report)
		case "replica-A", "replica-B":
			replicaReports = append(replicaReports, report)
		}
	}
	for _, expected := range []struct {
		name  string
		count int
	}{{"nginx", len(nginxReports)}, {"traefik", len(traefikReports)}, {"replica", len(replicaReports)}} {
		if expected.count != 1 {
			tree.Issues = append(tree.Issues, fmt.Sprintf("Expected one %s report; observed %d", expected.name, expected.count))
		}
	}
	if len(nginxReports) == 1 && len(traefikReports) == 1 && traefikReports[0].ParentID != nginxReports[0].OpID {
		tree.Issues = append(tree.Issues, "Traefik's parent is not the nginx operation")
	}
	if len(traefikReports) == 1 && len(replicaReports) == 1 && replicaReports[0].ParentID != traefikReports[0].OpID {
		tree.Issues = append(tree.Issues, "Replica's parent is not the Traefik operation")
	}
	tree.Complete = len(reports) == 3 && len(tree.Issues) == 0
	return tree
}

var worktreeTemplate = template.Must(template.New("worktree").Funcs(template.FuncMap{
	"indent": func(depth int) int { return min(depth, 20) * 24 },
}).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>X-Trace worktree</title>
<style>
body{font:16px system-ui;max-width:1100px;margin:32px auto;padding:0 20px;color:#263238}
code{font-size:13px;overflow-wrap:anywhere}
.worktree-row{display:grid;grid-template-columns:minmax(240px,2fr) minmax(0,3fr);gap:24px;border:1px solid #dfe5e8;border-radius:8px;padding:16px;margin:12px 0}
.node{min-width:0;border-left:3px solid #78909c;padding-left:12px;margin-left:var(--indent)}
.edge{font-size:13px;color:#546e7a}
.ids{margin-top:8px;font-size:14px}
summary{cursor:pointer}
.ids dl{margin-bottom:0}
.report{min-width:0}
.timestamp{margin:0 0 12px}
.timestamp-label,.empty-details{font-size:14px;color:#546e7a}
.report-details{margin:0}
.missing{border-color:#b45309;background:#fff7ed}
.missing .node{border-color:#b45309}
.missing-note{margin:0}
dt{font-weight:bold}
dd{margin:0 0 6px}
button{font:inherit}
@media(max-width:700px){
  body{margin:20px auto;padding:0 12px}
  input{box-sizing:border-box;max-width:100%}
  .worktree-row{grid-template-columns:minmax(0,1fr);gap:16px;padding:12px}
  .node{margin-left:min(var(--indent),48px)}
}
</style>
</head>
<body>
<h1>X-Trace worktree</h1>
<form action="/worktree"><label>TaskID <input name="taskID" size="36" value="{{.TaskID}}"></label> <button>Open</button></form>
{{if .TaskID}}<p>Task <code>{{.TaskID}}</code> · <a href="/worktree?taskID={{.TaskID}}">Refresh reports</a></p>
{{if .Complete}}<p><strong>Expected chain observed (3 reports).</strong></p>{{else}}<p><strong>Incomplete observations.</strong></p>{{end}}
<p>Edges use OpID and ParentID, never timestamp order. Completeness checks this demo's three expected reports. Missing reports can be delayed or dropped, or a request may not have reached a hop; absence alone does not establish a fault.</p>
{{if .Issues}}<ul>{{range .Issues}}<li>{{.}}</li>{{end}}</ul>{{end}}
<h2>Causal tree and reports</h2>
{{range .Rows}}
<div class="worktree-row {{if .Missing}}missing{{end}}">
  <div class="node" style="--indent:{{indent .Depth}}px">
    {{if .Missing}}<strong>MISSING parent report</strong>{{else}}<strong>{{.Report.Service}}</strong> {{if .Report.EdgeType}}<span class="edge">{{.Report.EdgeType}}</span>{{end}}{{end}}
    <details class="ids">
      <summary>Operation IDs</summary>
      <dl><dt>OpID</dt><dd><code>{{.Report.OpID}}</code></dd>{{if .Report.ParentID}}<dt>ParentID</dt><dd><code>{{.Report.ParentID}}</code></dd>{{end}}</dl>
    </details>
  </div>
  <div class="report">
    {{if .Missing}}
    <p class="missing-note">No report has arrived for this operation. Its timestamp and details are unavailable.</p>
    {{else}}
    <p class="timestamp"><span class="timestamp-label">Local report timestamp</span><br><code>{{.Report.Timestamp}}</code></p>
    {{if .Report.Details}}<dl class="report-details">{{range $key,$value := .Report.Details}}<dt>{{$key}}</dt><dd><code>{{$value}}</code></dd>{{end}}</dl>{{else}}<p class="empty-details">No additional details.</p>{{end}}
    {{end}}
  </div>
</div>
{{else}}<p>No reports have arrived for this TaskID. Refresh after a moment.</p>{{end}}
{{else}}<p>Request a cat at <a href="http://localhost:8080">localhost:8080</a>, then follow its worktree link or paste its TaskID here.</p>{{end}}
</body>
</html>`))

func (c *Collector) showWorktree(w http.ResponseWriter, r *http.Request) {
	taskID := r.URL.Query().Get("taskID")
	if taskID != "" && !trace.ValidID(taskID) {
		http.Error(w, "taskID must be 32 lowercase hexadecimal characters", http.StatusBadRequest)
		return
	}
	var reports []trace.Report
	if taskID != "" {
		var err error
		reports, err = c.reports(r, taskID)
		if err != nil {
			log.Printf("read worktree: %v", err)
			http.Error(w, "cannot read reports", http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := worktreeTemplate.Execute(w, reconstruct(taskID, reports)); err != nil {
		log.Printf("render worktree: %v", err)
	}
}
