local cjson = require "cjson.safe"
local random = require "resty.random"
local hex = require "resty.string".to_hex

local bytes, random_error = random.bytes(16, true)
if not bytes then
    ngx.log(ngx.ERR, "cannot create operation ID: ", random_error)
    return ngx.exit(500)
end

-- Nginx is the trace boundary: each cat request gets a new TaskID.
local task_id = ngx.var.request_id
local op_id = hex(bytes)
ngx.var.trace_task_id = task_id
ngx.var.trace_op_id = op_id

local payload = cjson.encode({
    taskID = task_id, opID = op_id, parentID = "", edgeType = "",
    service = "nginx",
    timestamp = ngx.utctime():gsub(" ", "T") .. "Z",
    details = {path = ngx.var.uri, proxy_cache = "disabled"}
})

local function send_report(premature, body)
    if premature then return end
    -- Timer callbacks may use cosockets; no request waits for this delivery.
    local socket = ngx.socket.tcp()
    socket:settimeout(300)
    local ok, err = socket:connect("collector", 8080)
    if ok then
        ok, err = socket:send("POST /reports HTTP/1.0\r\nHost: collector\r\n" ..
            "Content-Type: application/json\r\nConnection: close\r\n" ..
            "Content-Length: " .. #body .. "\r\n\r\n" .. body)
    end
    if ok then
        local status
        status, err = socket:receive("*l")
        ok = status and (status:find(" 201 ", 1, true) or status:find(" 200 ", 1, true))
        if not ok and status then err = status end
    end
    socket:close()
    if not ok then ngx.log(ngx.WARN, "nginx report delivery failed: ", err) end
end

-- Limits in nginx.conf bound both pending and running report timers.
local ok, err = ngx.timer.at(0, send_report, payload)
if not ok then ngx.log(ngx.WARN, "nginx report dropped: ", err) end
